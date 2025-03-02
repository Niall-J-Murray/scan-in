document.addEventListener('DOMContentLoaded', function() {
    // Smooth scrolling for anchor links
    document.querySelectorAll('a[href^="#"]').forEach(anchor => {
        anchor.addEventListener('click', function(e) {
            e.preventDefault();
            const targetId = this.getAttribute('href');
            if (targetId === '#') return;
            
            const targetElement = document.querySelector(targetId);
            if (targetElement) {
                window.scrollTo({
                    top: targetElement.offsetTop - 80,
                    behavior: 'smooth'
                });
            }
        });
    });

    // Initialize upload area
    const uploadArea = document.getElementById('upload-area');
    const fileInput = document.getElementById('file-input');
    const progressContainer = document.getElementById('progress-container');
    const progressBar = document.getElementById('progress-bar');
    const resultContainer = document.getElementById('result-container');
    const documentPreview = document.getElementById('document-preview');
    const documentImage = document.getElementById('document-image');
    const scanAnotherBtn = document.getElementById('scan-another');
    const vendorNameField = document.getElementById('vendor-name');
    const invoiceNumberField = document.getElementById('invoice-number');
    const invoiceDateField = document.getElementById('invoice-date');
    const totalAmountField = document.getElementById('total-amount');
    const currencyField = document.getElementById('currency');
    const browseLink = document.querySelector('.browse-link');

    // Prevent default drag behaviors
    ['dragenter', 'dragover', 'dragleave', 'drop'].forEach(eventName => {
        uploadArea.addEventListener(eventName, preventDefaults, false);
        document.body.addEventListener(eventName, preventDefaults, false);
    });

    // Highlight drop area when item is dragged over it
    ['dragenter', 'dragover'].forEach(eventName => {
        uploadArea.addEventListener(eventName, highlight, false);
    });

    ['dragleave', 'drop'].forEach(eventName => {
        uploadArea.addEventListener(eventName, unhighlight, false);
    });

    // Handle dropped files
    uploadArea.addEventListener('drop', handleDrop, false);

    // Handle browse files click
    browseLink.addEventListener('click', function() {
        fileInput.click();
    });

    // Handle file input change
    fileInput.addEventListener('change', function() {
        handleFiles(this.files);
    });

    // Handle scan another button
    scanAnotherBtn.addEventListener('click', function() {
        resetUI();
    });

    function preventDefaults(e) {
        e.preventDefault();
        e.stopPropagation();
    }

    function highlight() {
        uploadArea.classList.add('dragover');
    }

    function unhighlight() {
        uploadArea.classList.remove('dragover');
    }

    function handleDrop(e) {
        const dt = e.dataTransfer;
        const files = dt.files;
        
        if (files.length > 0) {
            const file = files[0];
            if (file.type.startsWith('image/')) {
                handleFiles(files);
            } else {
                showError('Please upload an image file');
            }
        }
    }

    function handleFiles(files) {
        const file = files[0];
        
        // Show progress
        uploadArea.style.display = 'none';
        progressContainer.classList.remove('d-none');
        
        // Create FormData
        const formData = new FormData();
        formData.append('invoice', file);
        
        // Upload file
        uploadFile(formData);
    }

    function uploadFile(formData) {
        const xhr = new XMLHttpRequest();
        
        // Progress tracking
        xhr.upload.addEventListener('progress', function(e) {
            if (e.lengthComputable) {
                const percentComplete = (e.loaded / e.total) * 100;
                progressBar.style.width = percentComplete + '%';
            }
        });
        
        xhr.addEventListener('load', function() {
            if (xhr.status === 200) {
                try {
                    const response = JSON.parse(xhr.responseText);
                    displayResults(response);
                } catch (e) {
                    showError('Error parsing response');
                }
            } else {
                showError('Error uploading file');
            }
        });
        
        xhr.addEventListener('error', function() {
            showError('Network error');
        });
        
        xhr.open('POST', '/scan-invoice');
        xhr.send(formData);
    }

    function displayResults(data) {
        // Hide progress
        progressContainer.classList.add('d-none');
        
        // Show results
        resultContainer.classList.remove('d-none');
        
        // Populate data
        vendorNameField.textContent = data.invoice.VendorName || 'Not detected';
        invoiceNumberField.textContent = data.invoice.InvoiceNumber || 'Not detected';
        invoiceDateField.textContent = data.invoice.Date || 'Not detected';
        
        // Format the total amount with 2 decimal places
        const amount = parseFloat(data.invoice.TotalAmount);
        totalAmountField.textContent = amount ? amount.toFixed(2) : 'Not detected';
        
        // Display the currency
        currencyField.textContent = data.invoice.Currency || 'Not detected';
        
        // Show document preview if available
        if (data.processed_image_url) {
            documentPreview.classList.remove('d-none');
            documentImage.src = data.processed_image_url;
        }
        
        // Scroll to results
        setTimeout(() => {
            resultContainer.scrollIntoView({ behavior: 'smooth', block: 'start' });
        }, 300);

        // Add animation to the results
        animateResults();
    }

    function resetUI() {
        // Hide results and preview
        resultContainer.classList.add('d-none');
        documentPreview.classList.add('d-none');
        
        // Show upload area
        uploadArea.style.display = 'block';
        
        // Reset progress
        progressBar.style.width = '0%';
        
        // Scroll to upload area
        setTimeout(() => {
            document.getElementById('upload-section').scrollIntoView({ behavior: 'smooth', block: 'start' });
        }, 300);
    }

    function showError(message) {
        // Hide progress
        progressContainer.classList.add('d-none');
        
        // Show upload area
        uploadArea.style.display = 'block';
        
        // Show error message
        alert(message);
    }

    function animateResults() {
        const tableRows = document.querySelectorAll('#result-container table tr');
        tableRows.forEach((row, index) => {
            row.style.opacity = '0';
            row.style.transform = 'translateY(20px)';
            row.style.transition = 'opacity 0.3s ease, transform 0.3s ease';
            
            setTimeout(() => {
                row.style.opacity = '1';
                row.style.transform = 'translateY(0)';
            }, 100 * (index + 1));
        });
    }

    // Add some initial animations
    function animateHeroSection() {
        const heroTitle = document.querySelector('.hero-title');
        const heroSubtitle = document.querySelector('.hero-subtitle');
        const getStartedBtn = document.querySelector('.get-started-btn');
        const heroImage = document.querySelector('.hero-image');
        
        if (heroTitle) {
            heroTitle.style.opacity = '0';
            heroTitle.style.transform = 'translateY(30px)';
            heroTitle.style.transition = 'opacity 0.8s ease, transform 0.8s ease';
            setTimeout(() => {
                heroTitle.style.opacity = '1';
                heroTitle.style.transform = 'translateY(0)';
            }, 300);
        }
        
        if (heroSubtitle) {
            heroSubtitle.style.opacity = '0';
            heroSubtitle.style.transform = 'translateY(30px)';
            heroSubtitle.style.transition = 'opacity 0.8s ease, transform 0.8s ease';
            setTimeout(() => {
                heroSubtitle.style.opacity = '1';
                heroSubtitle.style.transform = 'translateY(0)';
            }, 500);
        }
        
        if (getStartedBtn) {
            getStartedBtn.style.opacity = '0';
            getStartedBtn.style.transform = 'translateY(30px)';
            getStartedBtn.style.transition = 'opacity 0.8s ease, transform 0.8s ease';
            setTimeout(() => {
                getStartedBtn.style.opacity = '1';
                getStartedBtn.style.transform = 'translateY(0)';
            }, 700);
        }
        
        if (heroImage) {
            heroImage.style.opacity = '0';
            heroImage.style.transform = 'translateX(30px)';
            heroImage.style.transition = 'opacity 0.8s ease, transform 0.8s ease';
            setTimeout(() => {
                heroImage.style.opacity = '1';
                heroImage.style.transform = 'translateX(0)';
            }, 900);
        }
    }
    
    // Run initial animations
    animateHeroSection();
}); 