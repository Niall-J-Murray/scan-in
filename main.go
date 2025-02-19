package main

import (
	"fmt"
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
	"gorm.io/driver/postgres"
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
	// TODO: Implement invoice scanning and parsing logic
	c.JSON(200, gin.H{"message": "Invoice scanned and processed"})
}

func getInvoices(c *gin.Context) {
	var invoices []Invoice
	db.Find(&invoices)
	c.JSON(200, invoices)
}
