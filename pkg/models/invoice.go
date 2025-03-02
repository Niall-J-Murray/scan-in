package models

import (
	"gorm.io/gorm"
)

// Invoice represents an invoice document with extracted information
type Invoice struct {
	gorm.Model
	InvoiceNumber string
	Date          string
	TotalAmount   float64
	Currency      string
	VendorName    string
}

// TextLine represents a line of text with its position from OCR
type TextLine struct {
	Text   string
	X      int
	Y      int
	Width  int
	Height int
}
