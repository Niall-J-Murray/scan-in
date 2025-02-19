package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/services/cognitiveservices/v3.0/computervision"
	"github.com/Azure/go-autorest/autorest"
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

	// Define routes
	r.POST("/scan-invoice", scanInvoice)
	r.GET("/invoices", getInvoices)

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

	// Create the client
	client := computervision.New(os.Getenv("AZURE_ENDPOINT"))
	auth := autorest.NewCognitiveServicesAuthorizer(os.Getenv("AZURE_API_KEY"))
	client.Authorizer = auth

	// Read the image file
	imageData, err := os.ReadFile(tempPath)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to read file"})
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

	// Combine all text
	var text strings.Builder
	for _, region := range *result.Regions {
		for _, line := range *region.Lines {
			for _, word := range *line.Words {
				text.WriteString(*word.Text)
				text.WriteString(" ")
			}
			text.WriteString("\n")
		}
	}

	// Parse the extracted text
	invoice := parseInvoiceText(text.String())

	// Save to database
	if result := db.Create(&invoice); result.Error != nil {
		c.JSON(500, gin.H{"error": "Failed to save invoice"})
		return
	}

	c.JSON(200, gin.H{
		"message":  "Invoice scanned and processed",
		"invoice":  invoice,
		"raw_text": text.String(),
	})
}

func parseInvoiceText(text string) Invoice {
	// Convert text to lowercase for easier matching
	text = strings.ToLower(text)
	lines := strings.Split(text, "\n")

	invoice := Invoice{
		InvoiceNumber: extractInvoiceNumber(text),
		Date:          extractDate(text),
		TotalAmount:   extractAmount(text),
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
