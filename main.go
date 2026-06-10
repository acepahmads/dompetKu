package main

import (
	"log"
	"net/http"
	"strings"
)

func main() {
	log.Println("Starting DompetKu backend API server...")
	
	// Connect to database and migrate schemas
	InitDB()
	defer DB.Close()

	// 1. Chat routes
	http.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		ProcessChat(w, r)
	})

	http.HandleFunc("/api/whatsapp/webhook", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		WhatsAppWebhook(w, r)
	})

	http.HandleFunc("/api/chat-history", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		GetChatHistory(w, r)
	})

	// 2. Transaction routes
	http.HandleFunc("/api/transactions", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		if r.Method == "POST" {
			CreateTransaction(w, r)
		} else {
			GetTransactions(w, r)
		}
	})

	http.HandleFunc("/api/transactions/", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		path := r.URL.Path
		if r.Method == "POST" && strings.HasSuffix(path, "/reset") {
			ResetTransactions(w, r)
		} else if r.Method == "PUT" && strings.HasSuffix(path, "/status") {
			UpdateTransactionStatus(w, r)
		} else if r.Method == "DELETE" {
			DeleteTransaction(w, r)
		} else {
			http.NotFound(w, r)
		}
	})

	// 3. Analytics & reports routes
	http.HandleFunc("/api/financials", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		GetFinancials(w, r)
	})

	http.HandleFunc("/api/reports/", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		GenerateReport(w, r)
	})

	http.HandleFunc("/api/download/pdf", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		HandleDownloadPDF(w, r)
	})

	http.HandleFunc("/api/download/xls", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		HandleDownloadXLS(w, r)
	})

	http.HandleFunc("/api/download/word", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		HandleDownloadWord(w, r)
	})

	http.HandleFunc("/api/categories", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		GetCategories(w, r)
	})

	http.HandleFunc("/api/delete-triggers", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		GetDeleteTriggers(w, r)
	})

	http.HandleFunc("/api/salary-day-triggers", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		GetSalaryDayTriggers(w, r)
	})

	// 4. Planned keywords routes
	http.HandleFunc("/api/planned-keywords", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		if r.Method == "POST" {
			CreatePlannedKeyword(w, r)
		} else {
			GetPlannedKeywords(w, r)
		}
	})

	http.HandleFunc("/api/planned-keywords/", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		if r.Method == "DELETE" {
			DeletePlannedKeyword(w, r)
		} else {
			http.NotFound(w, r)
		}
	})

	// 5. Recurring templates routes
	http.HandleFunc("/api/recurring-templates", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		GetRecurringTemplates(w, r)
	})

	http.HandleFunc("/api/recurring-templates/", func(w http.ResponseWriter, r *http.Request) {
		enableCors(&w)
		if r.Method == "OPTIONS" {
			return
		}
		if r.Method == "DELETE" {
			DeleteRecurringTemplate(w, r)
		} else {
			http.NotFound(w, r)
		}
	})

	log.Println("Backend API running on http://localhost:8085")
	if err := http.ListenAndServe(":8085", nil); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
