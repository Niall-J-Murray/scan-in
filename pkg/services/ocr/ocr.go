package ocr

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"os"
	"strconv"
	"strings"

	"scan-in/pkg/models"

	"github.com/Azure/azure-sdk-for-go/services/cognitiveservices/v3.0/computervision"
	"github.com/Azure/go-autorest/autorest"
	"github.com/disintegration/imaging"
)

// Service handles OCR operations
type Service struct {
	client      *computervision.BaseClient
	apiEndpoint string
	apiKey      string
}

// NewService creates a new OCR service
func NewService(endpoint, apiKey string) *Service {
	client := computervision.New(endpoint)
	auth := autorest.NewCognitiveServicesAuthorizer(apiKey)
	client.Authorizer = auth

	return &Service{
		client:      &client,
		apiEndpoint: endpoint,
		apiKey:      apiKey,
	}
}

// EnhanceImageForOCR enhances the image for better OCR results
func (s *Service) EnhanceImageForOCR(imagePath string) (string, error) {
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

// CreateDisplayImage creates a cropped and enhanced version of the invoice for display
func (s *Service) CreateDisplayImage(sourcePath, destPath string) error {
	// Open the source image
	src, err := imaging.Open(sourcePath)
	if err != nil {
		return err
	}

	// Get image dimensions
	width := src.Bounds().Dx()
	height := src.Bounds().Dy()

	// Convert to grayscale for edge detection
	gray := imaging.Grayscale(src)

	// Apply Gaussian blur to reduce noise
	blurred := imaging.Blur(gray, 1.0)

	// Apply edge detection (using contrast enhancement as a simple approach)
	edges := imaging.AdjustContrast(blurred, 50)
	edges = imaging.Invert(edges)

	// Find the document boundaries
	// This is a simplified approach to find the largest contour
	// In a real-world application, you would use more sophisticated contour detection

	// For now, we'll use a heuristic approach to find the document
	// We'll scan from the edges and find where the document likely begins

	// Define margins to crop (percentage of image size)
	topMargin := int(float64(height) * 0.05)
	bottomMargin := int(float64(height) * 0.05)
	leftMargin := int(float64(width) * 0.05)
	rightMargin := int(float64(width) * 0.05)

	// Create a cropped version of the original image
	cropped := imaging.Crop(src, image.Rect(leftMargin, topMargin, width-rightMargin, height-bottomMargin))

	// Enhance the cropped image
	img := imaging.AdjustContrast(cropped, 20)
	img = imaging.Sharpen(img, 1.0)
	img = imaging.AdjustBrightness(img, 5)

	// Resize if the image is too large
	if width > 1000 || height > 1000 {
		img = imaging.Fit(img, 1000, 1000, imaging.Lanczos)
	}

	// Save the processed image
	err = imaging.Save(img, destPath)
	if err != nil {
		return err
	}

	return nil
}

// ExtractText performs OCR on an image and returns the extracted text lines
func (s *Service) ExtractText(imagePath string) ([]models.TextLine, error) {
	// Read the processed image file
	imageData, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read processed file: %v", err)
	}

	// Create a ReadCloser from the image data
	imageReader := io.NopCloser(bytes.NewReader(imageData))

	// Extract text
	result, err := s.client.RecognizePrintedTextInStream(
		context.Background(),
		true,
		imageReader,
		computervision.OcrLanguages(computervision.En),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to extract text: %v", err)
	}

	// Extract text from the OCR result
	return extractTextFromOCRResult(result), nil
}

// extractTextFromOCRResult extracts text lines with position information from OCR result
func extractTextFromOCRResult(result computervision.OcrResult) []models.TextLine {
	var textLines []models.TextLine
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
				textLines = append(textLines, models.TextLine{
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
