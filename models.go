package main

import "time"

// Transaction represents a financial transaction record
type Transaction struct {
	ID        string     `json:"id" db:"id"`
	UserID    string     `json:"user_id" db:"user_id"`
	Tanggal   string     `json:"tanggal" db:"tanggal"` // Format: YYYY-MM-DD
	Deskripsi string     `json:"deskripsi" db:"deskripsi"`
	Kategori  string     `json:"kategori" db:"kategori"`
	Tipe      string     `json:"tipe" db:"tipe"` // "income" or "expense"
	Nominal   float64    `json:"nominal" db:"nominal"`
	Status    string     `json:"status" db:"status"` // "planned" or "paid"
	DueDate   *string    `json:"due_date,omitempty" db:"due_date"` // Optional, Format: YYYY-MM-DD
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
}

// ChatMessage represents a record in the chat history
type ChatMessage struct {
	ID        int       `json:"id" db:"id"`
	Sender    string    `json:"sender" db:"sender"` // "user" or "bot"
	Message   string    `json:"message" db:"message"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// KPISummary aggregates current balances and statuses
type KPISummary struct {
	Saldo            float64 `json:"saldo"`
	TotalTagihan     float64 `json:"total_tagihan"`
	TagihanCount     int     `json:"tagihan_count"`
	SisaAman         float64 `json:"sisa_aman"`
	TotalPengeluaran float64 `json:"total_pengeluaran"`
}

// ChartDataPoint maps category spending sums
type ChartDataPoint struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// PlannedKeyword represents a user keyword that determines if a transaction is planned
type PlannedKeyword struct {
	ID        int       `json:"id"`
	Keyword   string    `json:"keyword"`
	CreatedAt time.Time `json:"created_at"`
}

// TransactionCategory represents a category record in the database
type TransactionCategory struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Tipe      string    `json:"tipe"`
	Keywords  string    `json:"keywords"`
	CreatedAt time.Time `json:"created_at"`
}

// RecurringTemplate represents a recurring template record
type RecurringTemplate struct {
	ID        string    `json:"id" db:"id"`
	UserID    string    `json:"user_id" db:"user_id"`
	Tipe      string    `json:"tipe" db:"tipe"`
	Kategori  string    `json:"kategori" db:"kategori"`
	Nominal   float64   `json:"nominal" db:"nominal"`
	Deskripsi string    `json:"deskripsi" db:"deskripsi"`
	TargetDay int       `json:"target_day" db:"target_day"`
	Status    string    `json:"status" db:"status"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}
