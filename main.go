package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"image/color"

	"github.com/Azure/azure-sdk-for-go/services/cognitiveservices/v3.0/computervision"
	"github.com/Azure/go-autorest/autorest"
	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type Invoice struct {
	gorm.Model
	InvoiceNumber string
	Date          string
	TotalAmount   float64
	Currency      string
	VendorName    string
}

var db *gorm.DB

func main() {
	// Load environment variables
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	// Set up database connection
	dbURL := os.Getenv("DATABASE_URL")
	db, err = gorm.Open(postgres.Open(dbURL), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database")
	}

	// Auto migrate the schema
	db.AutoMigrate(&Invoice{})

	// Set up Gin router
	r := gin.Default()

	// Serve static files
	r.Static("/static", "./web/static")

	// Load HTML templates
	r.LoadHTMLGlob("web/templates/*")

	// Define routes
	r.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.html", gin.H{
			"title": "Invoice Scanner",
		})
	})

	r.POST("/scan-invoice", scanInvoice)
	r.GET("/invoices", getInvoices)

	// Start the image cleanup goroutine
	go cleanupOldImages()

	// Start the server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}

func scanInvoice(c *gin.Context) {
	// Get the file from the request
	file, err := c.FormFile("invoice")
	if err != nil {
		c.JSON(400, gin.H{"error": "No file uploaded"})
		return
	}

	// Save the uploaded file temporarily
	tempPath := "temp-invoice.jpg"
	if err := c.SaveUploadedFile(file, tempPath); err != nil {
		c.JSON(500, gin.H{"error": "Failed to save file"})
		return
	}
	defer os.Remove(tempPath)

	// Process the image to enhance it for OCR
	processedPath, err := enhanceImageForOCR(tempPath)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to process image: " + err.Error()})
		return
	}
	defer os.Remove(processedPath)

	// Create a unique filename for the display image using a timestamp
	timestamp := time.Now().UnixNano()
	displayFilename := fmt.Sprintf("processed-invoice-%d.jpg", timestamp)
	displayPath := fmt.Sprintf("web/static/img/%s", displayFilename)

	// Create a cropped version for display
	if err := createDisplayImage(tempPath, displayPath); err != nil {
		log.Printf("Warning: Failed to create display image: %v", err)
		// Continue processing even if display image creation fails
	}

	// Create the client
	client := computervision.New(os.Getenv("AZURE_ENDPOINT"))
	auth := autorest.NewCognitiveServicesAuthorizer(os.Getenv("AZURE_API_KEY"))
	client.Authorizer = auth

	// Read the processed image file
	imageData, err := os.ReadFile(processedPath)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to read processed file"})
		return
	}

	// Create a ReadCloser from the image data
	imageReader := io.NopCloser(bytes.NewReader(imageData))

	// Extract text
	result, err := client.RecognizePrintedTextInStream(
		context.Background(),
		true,
		imageReader,
		computervision.OcrLanguages(computervision.En),
	)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to extract text"})
		return
	}

	// Extract text from the OCR result
	textLines := extractTextFromOCRResult(result)

	// Extract invoice details
	invoice := extractInvoiceDetails(textLines)

	// Save the invoice to the database
	if err := db.Create(&invoice).Error; err != nil {
		log.Printf("Warning: Failed to save invoice to database: %v", err)
		// Continue even if database save fails
	}

	// Return the invoice data and processed image URL with the unique filename
	c.JSON(200, gin.H{
		"invoice":             invoice,
		"processed_image_url": fmt.Sprintf("/static/img/%s", displayFilename),
	})
}

// TextLine represents a line of text with its position
type TextLine struct {
	Text   string
	X      int
	Y      int
	Width  int
	Height int
}

// enhanceImageForOCR enhances the image for better OCR results
func enhanceImageForOCR(imagePath string) (string, error) {
	// Open the image
	src, err := imaging.Open(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to open image: %v", err)
	}

	// Apply a series of image processing operations to enhance the document
	// 1. Convert to grayscale for better contrast
	img := imaging.Grayscale(src)

	// 2. Increase contrast more aggressively
	img = imaging.AdjustContrast(img, 30)

	// 3. Sharpen the image to make text more readable
	img = imaging.Sharpen(img, 1.5)

	// 4. Apply brightness adjustment
	img = imaging.AdjustBrightness(img, 10)

	// 5. Apply gamma correction to enhance details
	img = imaging.AdjustGamma(img, 1.2)

	// Save the processed image
	processedPath := "processed-invoice.jpg"
	err = imaging.Save(img, processedPath)
	if err != nil {
		return "", fmt.Errorf("failed to save processed image: %v", err)
	}

	return processedPath, nil
}

func parseInvoiceTextWithPosition(textLines []TextLine) Invoice {
	// Sort lines by Y position for top-to-bottom processing
	sort.Slice(textLines, func(i, j int) bool {
		return textLines[i].Y < textLines[j].Y
	})

	// Extract vendor name from the top lines (typically top left)
	vendorName := extractVendorNameFromPosition(textLines)

	// Extract invoice details
	invoiceNumber := extractInvoiceNumberFromPosition(textLines)
	date := extractDateFromPosition(textLines)
	totalAmount, currency := extractAmountFromPosition(textLines)

	invoice := Invoice{
		InvoiceNumber: invoiceNumber,
		Date:          date,
		TotalAmount:   totalAmount,
		Currency:      currency,
		VendorName:    vendorName,
	}

	return invoice
}

func extractVendorNameFromPosition(textLines []TextLine) string {
	// Look at the top 30% of the document for vendor name
	if len(textLines) == 0 {
		return "UNKNOWN"
	}

	// Find the maximum Y value to determine document height
	maxY := 0
	maxX := 0
	for _, line := range textLines {
		if line.Y > maxY {
			maxY = line.Y
		}
		if line.X > maxX {
			maxX = line.X
		}
	}

	// Consider the top 30% of the document
	topThreshold := maxY * 3 / 10

	// Consider the left half of the document for logo/company name
	leftHalfThreshold := maxX / 2

	// Find lines in the top area
	var topLines []TextLine
	var topLeftLines []TextLine
	var websiteDomains []string
	var emailDomains []string
	var domainMainParts []string // Store main parts of domains for comparison

	// Extract website and email domains from the entire document
	websiteRegex := regexp.MustCompile(`(?i)www\.([a-z0-9][-a-z0-9]*\.)+[a-z0-9][-a-z0-9]*`)
	emailRegex := regexp.MustCompile(`(?i)@([a-z0-9][-a-z0-9]*\.)+[a-z0-9][-a-z0-9]*`)
	domainRegex := regexp.MustCompile(`(?i)https?://([a-z0-9][-a-z0-9]*\.)+[a-z0-9][-a-z0-9]*`)

	for _, line := range textLines {
		// Extract website domains
		websiteMatches := websiteRegex.FindAllStringSubmatch(line.Text, -1)
		for _, match := range websiteMatches {
			if len(match) > 0 {
				domain := strings.TrimPrefix(match[0], "www.")
				websiteDomains = append(websiteDomains, domain)

				// Extract main part of domain
				parts := strings.Split(domain, ".")
				if len(parts) > 0 {
					domainMainParts = append(domainMainParts, parts[0])
				}
			}
		}

		// Extract email domains
		emailMatches := emailRegex.FindAllStringSubmatch(line.Text, -1)
		for _, match := range emailMatches {
			if len(match) > 1 {
				emailDomains = append(emailDomains, match[1])

				// Extract main part of domain
				parts := strings.Split(match[1], ".")
				if len(parts) > 0 {
					domainMainParts = append(domainMainParts, parts[0])
				}
			}
		}

		// Extract domains from URLs
		domainMatches := domainRegex.FindAllStringSubmatch(line.Text, -1)
		for _, match := range domainMatches {
			if len(match) > 1 {
				websiteDomains = append(websiteDomains, match[1])

				// Extract main part of domain
				parts := strings.Split(match[1], ".")
				if len(parts) > 0 {
					domainMainParts = append(domainMainParts, parts[0])
				}
			}
		}

		// Check if the line is in the top 30%
		if line.Y < topThreshold {
			topLines = append(topLines, line)

			// Check if it's also in the left half
			if line.X < leftHalfThreshold {
				topLeftLines = append(topLeftLines, line)
			}
		}
	}

	// Sort top-left lines by Y position (top to bottom)
	sort.Slice(topLeftLines, func(i, j int) bool {
		return topLeftLines[i].Y < topLeftLines[j].Y
	})

	// Sort top lines by Y position (top to bottom)
	sort.Slice(topLines, func(i, j int) bool {
		return topLines[i].Y < topLines[j].Y
	})

	// Look for potential logo text (usually larger text in the top-left)
	// We'll prioritize the first few lines in the top-left as they're likely to be the logo
	var logoTextCandidates []string

	// Take the first 3 lines from top-left as potential logo text
	for i, line := range topLeftLines {
		if i >= 3 {
			break
		}

		text := strings.TrimSpace(line.Text)
		if len(text) > 2 {
			logoTextCandidates = append(logoTextCandidates, text)
		}
	}

	// Look for address lines (typically start with numbers or contain "street", "avenue", etc.)
	var addressLines []string
	addressRegex := regexp.MustCompile(`(?i)(\d+\s+[a-z0-9\s,]+(?:street|st|avenue|ave|road|rd|boulevard|blvd|lane|ln|drive|dr|way|place|pl|court|ct))`)

	// Check the top-left lines for address patterns
	for _, line := range topLeftLines {
		if addressRegex.MatchString(line.Text) {
			addressLines = append(addressLines, line.Text)
		}
	}

	// If we have both domain parts and logo text, try to find matches
	if len(domainMainParts) > 0 && len(logoTextCandidates) > 0 {
		// For each domain part, check if it appears in any logo text
		for _, domainPart := range domainMainParts {
			// Clean the domain part for comparison
			cleanDomainPart := cleanTextForComparison(domainPart)

			for _, logoText := range logoTextCandidates {
				// Clean the logo text for comparison
				cleanLogoText := cleanTextForComparison(logoText)

				// Check if the domain part is contained in the logo text
				if strings.Contains(cleanLogoText, cleanDomainPart) {
					// Found a match between domain and logo text
					return logoText
				}

				// Check if logo text is contained in domain part
				if strings.Contains(cleanDomainPart, cleanLogoText) && len(logoText) > 3 {
					return logoText
				}
			}
		}
	}

	// If we have address lines, look for company name in the line before the address
	if len(addressLines) > 0 && len(topLeftLines) > 1 {
		// Find the index of the first address line in topLeftLines
		for i, addressLine := range addressLines {
			for j, line := range topLeftLines {
				if line.Text == addressLine && j > 0 {
					// The line before the address might be the company name
					potentialCompanyName := strings.TrimSpace(topLeftLines[j-1].Text)

					// Check if this potential company name matches any domain part
					if len(domainMainParts) > 0 {
						cleanCompanyName := cleanTextForComparison(potentialCompanyName)

						for _, domainPart := range domainMainParts {
							cleanDomainPart := cleanTextForComparison(domainPart)

							// If there's similarity between company name and domain
							if strings.Contains(cleanCompanyName, cleanDomainPart) ||
								strings.Contains(cleanDomainPart, cleanCompanyName) {
								return potentialCompanyName
							}
						}
					}

					// If no domain match but it looks like a company name, return it
					if len(potentialCompanyName) > 3 &&
						!strings.Contains(strings.ToLower(potentialCompanyName), "invoice") &&
						!strings.Contains(strings.ToLower(potentialCompanyName), "bill") {
						return potentialCompanyName
					}

					break
				}
			}

			// Only check the first address line
			if i == 0 {
				break
			}
		}
	}

	// If we have logo candidates, use the first one that's not a common header
	for _, logoText := range logoTextCandidates {
		lowerText := strings.ToLower(logoText)
		if !strings.Contains(lowerText, "invoice") &&
			!strings.Contains(lowerText, "bill") &&
			!strings.Contains(lowerText, "receipt") &&
			!strings.Contains(lowerText, "statement") {
			return logoText
		}
	}

	// If no good match found yet, try to extract from domains
	if len(websiteDomains) > 0 || len(emailDomains) > 0 {
		// Combine all domains
		allDomains := append(websiteDomains, emailDomains...)

		// Remove duplicates
		uniqueDomains := make(map[string]bool)
		for _, domain := range allDomains {
			uniqueDomains[domain] = true
		}

		// Convert domains to potential vendor names
		var domainBasedNames []string
		for domain := range uniqueDomains {
			// Extract the main part of the domain (before the TLD)
			parts := strings.Split(domain, ".")
			if len(parts) > 0 {
				mainPart := parts[0]

				// Convert domain name to a readable format
				// e.g., "acmecorp" -> "Acme Corp"
				readableName := convertDomainToReadableName(mainPart)
				domainBasedNames = append(domainBasedNames, readableName)
			}
		}

		// If we have domain-based names, return the longest one
		if len(domainBasedNames) > 0 {
			sort.Slice(domainBasedNames, func(i, j int) bool {
				return len(domainBasedNames[i]) > len(domainBasedNames[j])
			})
			return domainBasedNames[0]
		}
	}

	// If still no good candidate, fall back to the original approach
	// Look for the longest line in the top-left area
	var potentialVendorNames []string

	for _, line := range topLeftLines {
		text := strings.TrimSpace(line.Text)
		lowerText := strings.ToLower(text)

		// Skip lines that are likely to be headers or labels
		if len(text) > 3 &&
			!strings.Contains(lowerText, "invoice") &&
			!strings.Contains(lowerText, "bill") &&
			!strings.Contains(lowerText, "receipt") &&
			!strings.Contains(lowerText, "statement") &&
			!strings.Contains(lowerText, "account") &&
			!strings.Contains(lowerText, "date") &&
			!strings.Contains(lowerText, "number") {

			// Check if this is potentially a company name
			potentialVendorNames = append(potentialVendorNames, text)
		}
	}

	// If we found potential vendor names in the top-left, use the longest one
	if len(potentialVendorNames) > 0 {
		// Sort by length (longest first)
		sort.Slice(potentialVendorNames, func(i, j int) bool {
			return len(potentialVendorNames[i]) > len(potentialVendorNames[j])
		})

		// Return the longest name that's not just a single word
		for _, name := range potentialVendorNames {
			if len(strings.Fields(name)) > 1 {
				return name
			}
		}

		// If all are single words, return the longest one
		return potentialVendorNames[0]
	}

	// Final fallback: just return the first non-empty line from the top
	for _, line := range topLines {
		if len(strings.TrimSpace(line.Text)) > 3 {
			return strings.TrimSpace(line.Text)
		}
	}

	return "UNKNOWN"
}

// Helper function to clean text for comparison
func cleanTextForComparison(text string) string {
	// Convert to lowercase
	text = strings.ToLower(text)

	// Remove common business entity suffixes
	suffixes := []string{" inc", " llc", " ltd", " limited", " corp", " corporation", " co", " company"}
	for _, suffix := range suffixes {
		text = strings.TrimSuffix(text, suffix)
	}

	// Remove non-alphanumeric characters
	re := regexp.MustCompile(`[^a-z0-9]`)
	text = re.ReplaceAllString(text, "")

	return text
}

// Helper function to convert a domain name to a readable company name
func convertDomainToReadableName(domain string) string {
	// Common prefixes to remove
	prefixes := []string{"www", "mail", "info", "support", "contact", "sales"}

	// Check and remove common prefixes
	for _, prefix := range prefixes {
		if strings.HasPrefix(domain, prefix) {
			domain = strings.TrimPrefix(domain, prefix)
			break
		}
	}

	// Remove any non-alphanumeric characters
	re := regexp.MustCompile(`[^a-zA-Z0-9]`)
	domain = re.ReplaceAllString(domain, " ")

	// Split by potential word boundaries (camelCase, PascalCase, snake_case)
	re = regexp.MustCompile(`([a-z])([A-Z])`)
	domain = re.ReplaceAllString(domain, "$1 $2")

	// Trim spaces and split
	domain = strings.TrimSpace(domain)
	words := strings.Fields(domain)

	// Capitalize each word
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
		}
	}

	// Join words back together
	return strings.Join(words, " ")
}

func extractInvoiceNumberFromPosition(textLines []TextLine) string {
	// Common invoice number patterns
	patterns := []string{
		`(?i)inv[oice]*[-#\s.:]*([A-Za-z0-9-]+)`,
		`(?i)invoice\s*number[-#\s.:]*([A-Za-z0-9-]+)`,
		`(?i)invoice\s*#\s*([A-Za-z0-9-]+)`,
		`(?i)invoice\s*no\.?\s*([A-Za-z0-9-]+)`,
		`(?i)inv\s*no\.?\s*([A-Za-z0-9-]+)`,
		`(?i)order\s*number[-#\s.:]*([A-Za-z0-9-]+)`,
		`(?i)order\s*#\s*([A-Za-z0-9-]+)`,
		`(?i)order\s*no\.?\s*([A-Za-z0-9-]+)`,
		`(?i)reference\s*number[-#\s.:]*([A-Za-z0-9-]+)`,
		`(?i)reference\s*#\s*([A-Za-z0-9-]+)`,
		`(?i)reference\s*no\.?\s*([A-Za-z0-9-]+)`,
		`(?i)ref\s*no\.?\s*([A-Za-z0-9-]+)`,
		`(?i)#\s*([A-Za-z0-9-]+)`,
	}

	// Find the maximum X and Y values to determine document dimensions
	maxX, maxY := 0, 0
	for _, line := range textLines {
		if line.X > maxX {
			maxX = line.X
		}
		if line.Y > maxY {
			maxY = line.Y
		}
	}

	// Consider the top 30% of the document
	topThreshold := maxY * 3 / 10

	// Consider the right half of the document
	rightHalfThreshold := maxX / 2

	// First check top right area for invoice number
	for _, line := range textLines {
		if line.Y < topThreshold && line.X > rightHalfThreshold {
			lowerText := strings.ToLower(line.Text)
			// Check if line contains invoice-related keywords
			if strings.Contains(lowerText, "invoice") ||
				strings.Contains(lowerText, "inv") ||
				strings.Contains(lowerText, "number") ||
				strings.Contains(lowerText, "#") {

				for _, pattern := range patterns {
					re := regexp.MustCompile(pattern)
					if matches := re.FindStringSubmatch(line.Text); len(matches) > 1 {
						// Clean up the result
						result := strings.TrimSpace(matches[1])
						// If it's just a single character, it's probably not a valid invoice number
						if len(result) > 1 {
							return result
						}
					}
				}

				// If no match with patterns but line contains invoice keyword,
				// try to extract alphanumeric sequence that might be the invoice number
				re := regexp.MustCompile(`[A-Za-z0-9][-A-Za-z0-9]{2,}`)
				matches := re.FindAllString(line.Text, -1)
				for _, match := range matches {
					// Skip if it's just a common word
					lowerMatch := strings.ToLower(match)
					if lowerMatch != "invoice" && lowerMatch != "number" && lowerMatch != "inv" && len(match) > 2 {
						return match
					}
				}
			}
		}
	}

	// If not found in top right, look for lines containing "invoice" or "number" keywords anywhere
	for _, line := range textLines {
		lowerText := strings.ToLower(line.Text)
		if strings.Contains(lowerText, "invoice") ||
			strings.Contains(lowerText, "inv") ||
			strings.Contains(lowerText, "number") ||
			strings.Contains(lowerText, "order") ||
			strings.Contains(lowerText, "reference") {
			for _, pattern := range patterns {
				re := regexp.MustCompile(pattern)
				if matches := re.FindStringSubmatch(line.Text); len(matches) > 1 {
					// Clean up the result
					result := strings.TrimSpace(matches[1])
					// If it's just a single character, it's probably not a valid invoice number
					if len(result) > 1 {
						return result
					}
				}
			}
		}
	}

	// Consider lines in the top half
	topHalfThreshold := maxY / 2

	// Check top half for invoice number
	for _, line := range textLines {
		if line.Y < topHalfThreshold {
			for _, pattern := range patterns {
				re := regexp.MustCompile(pattern)
				if matches := re.FindStringSubmatch(line.Text); len(matches) > 1 {
					// Clean up the result
					result := strings.TrimSpace(matches[1])
					// If it's just a single character, it's probably not a valid invoice number
					if len(result) > 1 {
						return result
					}
				}
			}
		}
	}

	// If not found in top half, check the entire document
	for _, line := range textLines {
		for _, pattern := range patterns {
			re := regexp.MustCompile(pattern)
			if matches := re.FindStringSubmatch(line.Text); len(matches) > 1 {
				// Clean up the result
				result := strings.TrimSpace(matches[1])
				// If it's just a single character, it's probably not a valid invoice number
				if len(result) > 1 {
					return result
				}
			}
		}
	}

	return "UNKNOWN"
}

func extractDateFromPosition(textLines []TextLine) string {
	// Date patterns
	patterns := []string{
		`\d{1,2}[-/.]\d{1,2}[-/.]\d{2,4}`,
		`\d{4}[-/.]\d{1,2}[-/.]\d{1,2}`,
		`(?i)(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+\d{1,2}[,\s]+\d{2,4}`,
		`(?i)(january|february|march|april|may|june|july|august|september|october|november|december)\s+\d{1,2}[,\s]+\d{2,4}`,
		`(?i)\d{1,2}\s+(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+\d{2,4}`,
		`(?i)\d{1,2}\s+(january|february|march|april|may|june|july|august|september|october|november|december)\s+\d{2,4}`,
	}

	// First look for lines containing date-related keywords
	dateKeywords := []string{"date", "issued", "invoice date", "order date", "billing date"}

	for _, line := range textLines {
		lowerText := strings.ToLower(line.Text)

		// Check if line contains any date keyword
		containsDateKeyword := false
		for _, keyword := range dateKeywords {
			if strings.Contains(lowerText, keyword) {
				containsDateKeyword = true
				break
			}
		}

		if containsDateKeyword {
			for _, pattern := range patterns {
				re := regexp.MustCompile(pattern)
				if match := re.FindString(line.Text); match != "" {
					return match
				}
			}
		}
	}

	// Find the maximum Y value to determine document height
	maxY := 0
	for _, line := range textLines {
		if line.Y > maxY {
			maxY = line.Y
		}
	}

	// Consider lines in the top half
	topHalfThreshold := maxY / 2

	// Check top half for dates
	for _, line := range textLines {
		if line.Y < topHalfThreshold {
			for _, pattern := range patterns {
				re := regexp.MustCompile(pattern)
				if match := re.FindString(line.Text); match != "" {
					return match
				}
			}
		}
	}

	// If still not found, check the entire document
	for _, line := range textLines {
		for _, pattern := range patterns {
			re := regexp.MustCompile(pattern)
			if match := re.FindString(line.Text); match != "" {
				return match
			}
		}
	}

	return "UNKNOWN"
}

func extractAmountFromPosition(textLines []TextLine) (float64, string) {
	// Common total amount patterns with currency symbols
	patterns := []string{
		`(?i)total:?\s*([\$€£])\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)amount\s*due:?\s*([\$€£])\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)balance\s*due:?\s*([\$€£])\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)grand\s*total:?\s*([\$€£])\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)total\s*amount:?\s*([\$€£])\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)total\s*due:?\s*([\$€£])\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)invoice\s*total:?\s*([\$€£])\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)payment\s*due:?\s*([\$€£])\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)([\$€£])\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		// Additional patterns with currency after the amount
		`(?i)total:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`,
		`(?i)amount\s*due:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`,
		`(?i)balance\s*due:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`,
		`(?i)grand\s*total:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`,
		`(?i)total\s*amount:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`,
		`(?i)total\s*due:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`,
		`(?i)invoice\s*total:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`,
		`(?i)payment\s*due:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`,
		`(?i)(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`,
	}

	// Patterns without currency symbols (for fallback)
	patternsNoCurrency := []string{
		`(?i)total:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)amount\s*due:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)balance\s*due:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)grand\s*total:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)total\s*amount:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)total\s*due:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)invoice\s*total:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
		`(?i)payment\s*due:?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})`,
	}

	// Currency mapping
	currencyMap := map[string]string{
		"$":   "USD",
		"€":   "EUR",
		"£":   "GBP",
		"EUR": "EUR",
		"USD": "USD",
		"GBP": "GBP",
		"eur": "EUR",
		"usd": "USD",
		"gbp": "GBP",
	}

	// Check for currency mentions in the document
	documentCurrency := detectDocumentCurrency(textLines)

	// Default currency - use detected document currency if available
	currency := "EUR" // Default to EUR instead of USD
	if documentCurrency != "" {
		currency = documentCurrency
	}

	// Find the maximum Y value to determine document height
	maxY := 0
	for _, line := range textLines {
		if line.Y > maxY {
			maxY = line.Y
		}
	}

	// First look for lines containing "total" or "amount" keywords
	for _, line := range textLines {
		lowerText := strings.ToLower(line.Text)
		if strings.Contains(lowerText, "total") ||
			strings.Contains(lowerText, "amount") ||
			strings.Contains(lowerText, "balance") ||
			strings.Contains(lowerText, "due") ||
			strings.Contains(lowerText, "payment") {

			// Try patterns with currency symbols first
			for _, pattern := range patterns {
				re := regexp.MustCompile(pattern)
				if matches := re.FindStringSubmatch(line.Text); len(matches) > 2 {
					// Check if this is a pattern with currency before or after the amount
					amountStr := ""
					currencySymbol := ""

					if strings.Contains(pattern, `(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`) {
						// Currency after amount
						amountStr = matches[1]
						currencySymbol = matches[2]
					} else {
						// Currency before amount
						currencySymbol = matches[1]
						amountStr = matches[2]
					}

					// Map currency symbol to currency code
					if mappedCurrency, ok := currencyMap[currencySymbol]; ok {
						currency = mappedCurrency
					}

					// Clean up the amount string - handle European number format (comma as decimal separator)
					amount, err := parseAmount(amountStr)
					if err == nil {
						return amount, currency
					}
				}
			}

			// If no match with currency, try patterns without currency
			for _, pattern := range patternsNoCurrency {
				re := regexp.MustCompile(pattern)
				if matches := re.FindStringSubmatch(line.Text); len(matches) > 1 {
					amountStr := matches[1]

					// Clean up the amount string
					amount, err := parseAmount(amountStr)
					if err == nil {
						// Look for currency symbols in the line
						if strings.Contains(line.Text, "$") {
							currency = "USD"
						} else if strings.Contains(line.Text, "€") || strings.Contains(strings.ToLower(line.Text), "eur") {
							currency = "EUR"
						} else if strings.Contains(line.Text, "£") || strings.Contains(strings.ToLower(line.Text), "gbp") {
							currency = "GBP"
						}
						return amount, currency
					}
				}
			}
		}
	}

	// Consider lines in the bottom 30% of the document for total amounts
	bottomThreshold := maxY * 7 / 10
	var largestAmount float64
	var largestAmountCurrency string

	for _, line := range textLines {
		if line.Y > bottomThreshold {
			// Try patterns with currency symbols first
			for _, pattern := range patterns {
				re := regexp.MustCompile(pattern)
				if matches := re.FindStringSubmatch(line.Text); len(matches) > 2 {
					// Check if this is a pattern with currency before or after the amount
					amountStr := ""
					currencySymbol := ""

					if strings.Contains(pattern, `(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})\s*([\$€£]|EUR|USD|GBP)`) {
						// Currency after amount
						amountStr = matches[1]
						currencySymbol = matches[2]
					} else {
						// Currency before amount
						currencySymbol = matches[1]
						amountStr = matches[2]
					}

					// Map currency symbol to currency code
					if mappedCurrency, ok := currencyMap[currencySymbol]; ok {
						currency = mappedCurrency
					}

					// Clean up the amount string
					amount, err := parseAmount(amountStr)
					if err == nil && amount > largestAmount {
						largestAmount = amount
						largestAmountCurrency = currency
					}
				}
			}

			// If no match with currency, try patterns without currency
			for _, pattern := range patternsNoCurrency {
				re := regexp.MustCompile(pattern)
				if matches := re.FindStringSubmatch(line.Text); len(matches) > 1 {
					amountStr := matches[1]

					// Clean up the amount string
					amount, err := parseAmount(amountStr)
					if err == nil && amount > largestAmount {
						largestAmount = amount

						// Look for currency symbols in the line
						if strings.Contains(line.Text, "$") {
							largestAmountCurrency = "USD"
						} else if strings.Contains(line.Text, "€") || strings.Contains(strings.ToLower(line.Text), "eur") {
							largestAmountCurrency = "EUR"
						} else if strings.Contains(line.Text, "£") || strings.Contains(strings.ToLower(line.Text), "gbp") {
							largestAmountCurrency = "GBP"
						} else {
							largestAmountCurrency = currency
						}
					}
				}
			}
		}
	}

	if largestAmount > 0 {
		return largestAmount, largestAmountCurrency
	}

	// Fallback: Find the largest number with a decimal point in the document
	var largestDecimalNumber float64
	var largestDecimalCurrency string

	for _, line := range textLines {
		// Look for numbers with decimal points
		re := regexp.MustCompile(`([\$€£])?\s*(\d{1,3}(?:[.,]\d{3})*[.,]\d{2})(?:\s*([\$€£]|EUR|USD|GBP))?`)
		matches := re.FindAllStringSubmatch(line.Text, -1)

		for _, match := range matches {
			currencySymbol := ""
			amountStr := ""

			if len(match) > 3 && match[3] != "" {
				// Currency after amount
				amountStr = match[2]
				currencySymbol = match[3]
			} else if len(match) > 2 && match[1] != "" {
				// Currency before amount
				currencySymbol = match[1]
				amountStr = match[2]
			} else if len(match) > 2 {
				amountStr = match[2]
			} else if len(match) > 1 {
				amountStr = match[1]
			}

			// Map currency symbol to currency code
			matchCurrency := currency
			if currencySymbol != "" {
				if mappedCurrency, ok := currencyMap[currencySymbol]; ok {
					matchCurrency = mappedCurrency
				}
			} else {
				// Look for currency symbols in the line
				if strings.Contains(line.Text, "$") {
					matchCurrency = "USD"
				} else if strings.Contains(line.Text, "€") || strings.Contains(strings.ToLower(line.Text), "eur") {
					matchCurrency = "EUR"
				} else if strings.Contains(line.Text, "£") || strings.Contains(strings.ToLower(line.Text), "gbp") {
					matchCurrency = "GBP"
				}
			}

			// Clean up the amount string
			amount, err := parseAmount(amountStr)
			if err == nil && amount > largestDecimalNumber {
				largestDecimalNumber = amount
				largestDecimalCurrency = matchCurrency
			}
		}
	}

	if largestDecimalNumber > 0 {
		return largestDecimalNumber, largestDecimalCurrency
	}

	return 0.0, currency
}

// Helper function to parse amount strings, handling different number formats
func parseAmount(amountStr string) (float64, error) {
	// First, try to determine if this is a European format (comma as decimal separator)
	// or US format (period as decimal separator)

	// Count commas and periods
	commaCount := strings.Count(amountStr, ",")
	periodCount := strings.Count(amountStr, ".")

	// Make a copy of the original string for processing
	processedStr := amountStr

	// Case 1: European format (e.g., 1.234,56)
	if (commaCount == 1 && periodCount >= 1) ||
		(commaCount == 1 && periodCount == 0) {
		// Last comma is the decimal separator
		lastCommaIndex := strings.LastIndex(processedStr, ",")
		if lastCommaIndex != -1 {
			// Replace the last comma with a period
			processedStr = processedStr[:lastCommaIndex] + "." + processedStr[lastCommaIndex+1:]
			// Remove all remaining periods (thousand separators)
			processedStr = strings.ReplaceAll(processedStr[:lastCommaIndex], ".", "")
		}
	} else if periodCount == 1 {
		// Case 2: US format (e.g., 1,234.56)
		// Remove all commas (thousand separators)
		processedStr = strings.ReplaceAll(processedStr, ",", "")
	} else if commaCount == 0 && periodCount == 0 {
		// Case 3: No separators, just digits
		// Nothing to do
	} else if periodCount > 1 {
		// Case 4: Multiple periods, assume the last one is the decimal separator
		lastPeriodIndex := strings.LastIndex(processedStr, ".")
		if lastPeriodIndex != -1 {
			// Keep only the last period
			processedStr = strings.ReplaceAll(processedStr[:lastPeriodIndex], ".", "") +
				processedStr[lastPeriodIndex:]
			// Remove all commas
			processedStr = strings.ReplaceAll(processedStr, ",", "")
		}
	} else if commaCount > 1 {
		// Case 5: Multiple commas, assume the last one is the decimal separator
		lastCommaIndex := strings.LastIndex(processedStr, ",")
		if lastCommaIndex != -1 {
			// Replace the last comma with a period
			processedStr = processedStr[:lastCommaIndex] + "." + processedStr[lastCommaIndex+1:]
			// Remove all remaining commas
			processedStr = strings.ReplaceAll(processedStr[:lastCommaIndex], ",", "")
		}
	}

	// Try to parse the processed string
	return strconv.ParseFloat(processedStr, 64)
}

// Helper function to detect the primary currency used in the document
func detectDocumentCurrency(textLines []TextLine) string {
	// Count occurrences of each currency
	currencyCount := map[string]int{
		"USD": 0,
		"EUR": 0,
		"GBP": 0,
	}

	// Look for currency symbols and codes
	for _, line := range textLines {
		text := line.Text
		lowerText := strings.ToLower(text)

		// Count currency symbols
		if strings.Contains(text, "$") {
			currencyCount["USD"]++
		}
		if strings.Contains(text, "€") {
			currencyCount["EUR"] += 2 // Give more weight to Euro symbol
		}
		if strings.Contains(text, "£") {
			currencyCount["GBP"]++
		}

		// Count currency codes
		if strings.Contains(lowerText, "usd") || strings.Contains(lowerText, "dollar") {
			currencyCount["USD"]++
		}
		if strings.Contains(lowerText, "eur") || strings.Contains(lowerText, "euro") {
			currencyCount["EUR"] += 2 // Give more weight to Euro mentions
		}
		if strings.Contains(lowerText, "gbp") || strings.Contains(lowerText, "pound") {
			currencyCount["GBP"]++
		}
	}

	// Find the most frequent currency
	maxCount := 0
	mostFrequentCurrency := "EUR" // Default to EUR if no currency is detected

	for currency, count := range currencyCount {
		if count > maxCount {
			maxCount = count
			mostFrequentCurrency = currency
		}
	}

	return mostFrequentCurrency
}

func parseInvoiceText(text string) Invoice {
	// Convert text to lowercase for easier matching
	text = strings.ToLower(text)
	lines := strings.Split(text, "\n")

	invoice := Invoice{
		InvoiceNumber: extractInvoiceNumber(text),
		Date:          extractDate(text),
		TotalAmount:   extractAmount(text),
		Currency:      "USD",
		VendorName:    extractVendorName(lines),
	}

	return invoice
}

func extractInvoiceNumber(text string) string {
	// Common invoice number patterns
	patterns := []string{
		`inv[oice]*[-#]?\s*([A-Za-z0-9-]+)`,
		`invoice\s*number[-#:]?\s*([A-Za-z0-9-]+)`,
		`#\s*([A-Za-z0-9-]+)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(strings.ToLower(text)); len(matches) > 1 {
			return matches[1]
		}
	}
	return "UNKNOWN"
}

func extractDate(text string) string {
	// Date patterns (you might want to add more patterns)
	patterns := []string{
		`\d{2}[-/]\d{2}[-/]\d{4}`,
		`\d{4}[-/]\d{2}[-/]\d{2}`,
		`(?i)(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\s+\d{1,2},?\s+\d{4}`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if match := re.FindString(text); match != "" {
			return match
		}
	}
	return "UNKNOWN"
}

func extractAmount(text string) float64 {
	// Look for total amount patterns
	patterns := []string{
		`total:?\s*[\$€£]?\s*(\d+[.,]\d{2})`,
		`amount\s*due:?\s*[\$€£]?\s*(\d+[.,]\d{2})`,
		`[\$€£]\s*(\d+[.,]\d{2})`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(strings.ToLower(text)); len(matches) > 1 {
			// Convert string to float64
			amount := strings.Replace(matches[1], ",", ".", 1)
			if val, err := strconv.ParseFloat(amount, 64); err == nil {
				return val
			}
		}
	}
	return 0.0
}

func extractVendorName(lines []string) string {
	// Usually, the vendor name is at the top of the invoice
	// This is a simple approach; you might need to adjust based on your invoices
	for i, line := range lines {
		if i < 5 && len(line) > 0 { // Check first 5 non-empty lines
			line = strings.TrimSpace(line)
			if len(line) > 3 && !strings.Contains(strings.ToLower(line), "invoice") {
				return line
			}
		}
	}
	return "UNKNOWN"
}

func getInvoices(c *gin.Context) {
	var invoices []Invoice
	db.Find(&invoices)
	c.JSON(200, invoices)
}

// createDisplayImage creates a cropped and enhanced version of the invoice for display
func createDisplayImage(sourcePath, destPath string) error {
	// Open the source image
	src, err := imaging.Open(sourcePath)
	if err != nil {
		return err
	}

	// Get image dimensions
	width := src.Bounds().Dx()
	height := src.Bounds().Dy()

	// Convert to grayscale for processing
	gray := imaging.Grayscale(src)

	// Apply moderate Gaussian blur to reduce noise
	blurred := imaging.Blur(gray, 1.5)

	// Apply Canny-like edge detection by using contrast and brightness adjustments
	edges := imaging.AdjustContrast(blurred, 50)
	edges = imaging.AdjustBrightness(edges, -10)

	// Create a binary threshold image
	binary := imaging.New(width, height, color.Gray{Y: 255})
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, _, _, _ := edges.At(x, y).RGBA()
			if r>>8 < 128 { // Threshold value
				binary.Set(x, y, color.Gray{Y: 0})
			}
		}
	}

	// Dilate the edges to connect nearby lines
	dilated := imaging.New(width, height, color.Gray{Y: 255})
	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			// Check 3x3 neighborhood
			hasBlackPixel := false
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					r, _, _, _ := binary.At(x+dx, y+dy).RGBA()
					if r>>8 < 128 {
						hasBlackPixel = true
						break
					}
				}
				if hasBlackPixel {
					break
				}
			}
			if hasBlackPixel {
				dilated.Set(x, y, color.Gray{Y: 0})
			}
		}
	}

	// Find the bounding box of the document
	minX, minY := width, height
	maxX, maxY := 0, 0

	// Scan for the document edges
	// We'll look for connected regions of dark pixels

	// First pass: find potential document edges
	edgePoints := make(map[string]bool)

	// Scan horizontally for edges
	for y := 0; y < height; y += height/50 + 1 { // Sample at intervals
		inDocument := false
		edgeCount := 0
		for x := 0; x < width; x++ {
			r, _, _, _ := dilated.At(x, y).RGBA()
			isEdge := r>>8 < 128

			if isEdge && !inDocument {
				inDocument = true
				edgePoints[fmt.Sprintf("%d,%d", x, y)] = true
				edgeCount++
			} else if isEdge && inDocument {
				// Still on an edge
				edgePoints[fmt.Sprintf("%d,%d", x, y)] = true
			} else if !isEdge && inDocument {
				inDocument = false
				edgePoints[fmt.Sprintf("%d,%d", x-1, y)] = true
				edgeCount++
			}
		}
	}

	// Scan vertically for edges
	for x := 0; x < width; x += width/50 + 1 { // Sample at intervals
		inDocument := false
		edgeCount := 0
		for y := 0; y < height; y++ {
			r, _, _, _ := dilated.At(x, y).RGBA()
			isEdge := r>>8 < 128

			if isEdge && !inDocument {
				inDocument = true
				edgePoints[fmt.Sprintf("%d,%d", x, y)] = true
				edgeCount++
			} else if isEdge && inDocument {
				// Still on an edge
				edgePoints[fmt.Sprintf("%d,%d", x, y)] = true
			} else if !isEdge && inDocument {
				inDocument = false
				edgePoints[fmt.Sprintf("%d,%d", x, y-1)] = true
				edgeCount++
			}
		}
	}

	// Calculate bounding box from edge points
	if len(edgePoints) > 0 {
		for pointStr := range edgePoints {
			var x, y int
			fmt.Sscanf(pointStr, "%d,%d", &x, &y)

			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}
		}
	} else {
		// Fallback if no edges detected
		minX = width / 20
		minY = height / 20
		maxX = width - width/20
		maxY = height - height/20
	}

	// Add margins to ensure we don't crop too tightly
	margin := 10
	minX = max(0, minX-margin)
	minY = max(0, minY-margin)
	maxX = min(width, maxX+margin)
	maxY = min(height, maxY+margin)

	// Sanity check - if the detected area is too small, use default margins
	if maxX-minX < width/3 || maxY-minY < height/3 {
		minX = width / 20
		minY = height / 20
		maxX = width - width/20
		maxY = height - height/20
	}

	// Crop the original image to the detected document area
	cropped := imaging.Crop(src, image.Rect(minX, minY, maxX, maxY))

	// Create a white background image
	result := imaging.New(maxX-minX, maxY-minY, color.White)

	// Copy the document onto the white background with slight transparency for background pixels
	for y := 0; y < result.Bounds().Dy(); y++ {
		for x := 0; x < result.Bounds().Dx(); x++ {
			// Get pixel from cropped image
			pixel := cropped.At(x, y)

			// Convert to grayscale value to check if it's background
			r, g, b, _ := pixel.RGBA()
			brightness := (r + g + b) / 3

			// If it's very bright (likely background), use white instead
			if brightness > 60000 { // Threshold for background detection
				result.Set(x, y, color.White)
			} else {
				result.Set(x, y, pixel)
			}
		}
	}

	// Apply mild enhancements to improve readability
	result = imaging.AdjustContrast(result, 10)
	result = imaging.Sharpen(result, 0.5)

	// Resize if the image is too large
	if width > 1000 || height > 1000 {
		result = imaging.Fit(result, 1000, 1000, imaging.Lanczos)
	}

	// Save the processed image
	err = imaging.Save(result, destPath)
	if err != nil {
		return err
	}

	return nil
}

// Helper function for Go versions before 1.21 which don't have built-in min for ints
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Helper function for Go versions before 1.21 which don't have built-in max for ints
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// extractTextFromOCRResult extracts text lines with position information from OCR result
func extractTextFromOCRResult(result computervision.OcrResult) []TextLine {
	var textLines []TextLine
	for _, region := range *result.Regions {
		for _, line := range *region.Lines {
			var lineText strings.Builder
			var boundingBox []int

			// Parse the bounding box
			if line.BoundingBox != nil {
				boundingBoxStr := *line.BoundingBox
				parts := strings.Split(boundingBoxStr, ",")
				for _, part := range parts {
					val, _ := strconv.Atoi(part)
					boundingBox = append(boundingBox, val)
				}
			}

			for _, word := range *line.Words {
				lineText.WriteString(*word.Text)
				lineText.WriteString(" ")
			}

			if len(boundingBox) >= 4 {
				textLines = append(textLines, TextLine{
					Text:   strings.TrimSpace(lineText.String()),
					X:      boundingBox[0],
					Y:      boundingBox[1],
					Width:  boundingBox[2],
					Height: boundingBox[3],
				})
			}
		}
	}
	return textLines
}

// extractInvoiceDetails extracts invoice details from text lines
func extractInvoiceDetails(textLines []TextLine) Invoice {
	vendorName := extractVendorNameFromPosition(textLines)
	invoiceNumber := extractInvoiceNumberFromPosition(textLines)
	date := extractDateFromPosition(textLines)
	totalAmount, currency := extractAmountFromPosition(textLines)

	invoice := Invoice{
		InvoiceNumber: invoiceNumber,
		Date:          date,
		TotalAmount:   totalAmount,
		Currency:      currency,
		VendorName:    vendorName,
	}

	return invoice
}

// cleanupOldImages removes processed invoice images older than the specified duration
func cleanupOldImages() {
	ticker := time.NewTicker(1 * time.Hour) // Run cleanup every hour
	defer ticker.Stop()

	for range ticker.C {
		log.Println("Running image cleanup...")
		cleanupImages()
	}
}

// cleanupImages removes processed invoice images older than 24 hours
func cleanupImages() {
	imgDir := "web/static/img"
	files, err := os.ReadDir(imgDir)
	if err != nil {
		log.Printf("Error reading image directory: %v", err)
		return
	}

	// Get current time
	now := time.Now()

	// Keep track of how many files were removed
	removedCount := 0

	for _, file := range files {
		// Skip if not a processed invoice image
		if !strings.HasPrefix(file.Name(), "processed-invoice-") {
			continue
		}

		// Get file info
		info, err := file.Info()
		if err != nil {
			log.Printf("Error getting file info for %s: %v", file.Name(), err)
			continue
		}

		// Check if file is older than 24 hours
		if now.Sub(info.ModTime()) > 24*time.Hour {
			// Remove the file
			err := os.Remove(filepath.Join(imgDir, file.Name()))
			if err != nil {
				log.Printf("Error removing old image %s: %v", file.Name(), err)
			} else {
				removedCount++
			}
		}
	}

	if removedCount > 0 {
		log.Printf("Cleaned up %d old processed images", removedCount)
	}
}
