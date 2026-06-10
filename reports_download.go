package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jung-kurt/gofpdf"
	"github.com/xuri/excelize/v2"
)

// Helper to parse query parameters and return range start and end dates in YYYY-MM-DD format
func parseDownloadParamsRange(r *http.Request) (string, string, string) {
	tipe := r.URL.Query().Get("tipe")
	if tipe == "" {
		tipe = "expense"
	}

	startDate := r.URL.Query().Get("start_date")
	endDate := r.URL.Query().Get("end_date")

	if startDate != "" && endDate != "" {
		return tipe, startDate, endDate
	}

	monthStr := r.URL.Query().Get("month")
	month := int(time.Now().Month())
	if m, err := strconv.Atoi(monthStr); err == nil && m >= 1 && m <= 12 {
		month = m
	}

	yearStr := r.URL.Query().Get("year")
	year := time.Now().Year()
	if y, err := strconv.Atoi(yearStr); err == nil && y >= 2000 {
		year = y
	}

	salaryDay := GetSalaryDay("user_1")
	start, end := getMonthCycleBounds(year, month, salaryDay)
	return tipe, start.Format("2006-01-02"), end.Format("2006-01-02")
}

type ReportTx struct {
	Tanggal   string
	Deskripsi string
	Kategori  string
	Tipe      string
	Nominal   float64
	Status    string
}

// Helper to fetch report transactions
func getReportTransactions(tipe string, startDate string, endDate string) ([]ReportTx, error) {
	var rows *sql.Rows
	var err error
	if tipe == "all" {
		rows, err = DB.Query("SELECT tanggal, deskripsi, kategori, tipe, nominal, status FROM transactions WHERE tanggal >= ? AND tanggal <= ? ORDER BY tanggal ASC, created_at ASC", startDate, endDate)
	} else {
		rows, err = DB.Query("SELECT tanggal, deskripsi, kategori, tipe, nominal, status FROM transactions WHERE tipe = ? AND tanggal >= ? AND tanggal <= ? ORDER BY tanggal ASC, created_at ASC", tipe, startDate, endDate)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Load categories map to lookup labels dynamically
	catLabels := map[string]string{}
	catRows, errCat := DB.Query("SELECT id, label FROM categories")
	if errCat == nil {
		defer catRows.Close()
		for catRows.Next() {
			var cid, clabel string
			if errScan := catRows.Scan(&cid, &clabel); errScan == nil {
				catLabels[cid] = clabel
			}
		}
	}

	var list []ReportTx
	for rows.Next() {
		var tx ReportTx
		if errScan := rows.Scan(&tx.Tanggal, &tx.Deskripsi, &tx.Kategori, &tx.Tipe, &tx.Nominal, &tx.Status); errScan == nil {
			if len(tx.Tanggal) >= 10 {
				tx.Tanggal = tx.Tanggal[:10]
			}
			tParts := strings.Split(tx.Tanggal, "-")
			if len(tParts) == 3 {
				tx.Tanggal = tParts[2] + "/" + tParts[1] + "/" + tParts[0]
			}
			if label, ok := catLabels[tx.Kategori]; ok {
				tx.Kategori = label
			} else {
				if len(tx.Kategori) > 0 {
					tx.Kategori = strings.ToUpper(tx.Kategori[:1]) + tx.Kategori[1:]
				}
			}
			list = append(list, tx)
		}
	}
	return list, nil
}

// HandleDownloadPDF generates a PDF file
func HandleDownloadPDF(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	tipe, startDate, endDate := parseDownloadParamsRange(r)
	list, err := getReportTransactions(tipe, startDate, endDate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Arial", "B", 14)

	periodStr := fmt.Sprintf("%s s/d %s", formatStrDateIndo(startDate), formatStrDateIndo(endDate))
	title := fmt.Sprintf("Laporan Rincian Pengeluaran (%s)", periodStr)
	if tipe == "income" {
		title = fmt.Sprintf("Laporan Rincian Pemasukan (%s)", periodStr)
	} else if tipe == "all" {
		title = fmt.Sprintf("Laporan Rincian Transaksi (%s)", periodStr)
	}

	pdf.Cell(190, 10, title)
	pdf.Ln(12)

	// Table headers
	pdf.SetFont("Arial", "B", 9)
	pdf.SetFillColor(240, 240, 240)
	pdf.CellFormat(25, 8, "Tanggal", "1", 0, "C", true, 0, "")
	pdf.CellFormat(70, 8, "Deskripsi", "1", 0, "L", true, 0, "")
	pdf.CellFormat(35, 8, "Kategori", "1", 0, "L", true, 0, "")
	pdf.CellFormat(30, 8, "Nominal", "1", 0, "R", true, 0, "")
	pdf.CellFormat(30, 8, "Status", "1", 1, "C", true, 0, "")

	pdf.SetFont("Arial", "", 8.5)
	var grandTotal float64
	for _, tx := range list {
		statusLabel := "Lunas"
		if tx.Status == "planned" {
			statusLabel = "Direncanakan"
		}

		nominalText := formatRupiah(tx.Nominal)
		if tipe == "all" {
			if tx.Tipe == "income" {
				nominalText = "+" + nominalText
				grandTotal += tx.Nominal
			} else {
				nominalText = "-" + nominalText
				grandTotal -= tx.Nominal
			}
		} else {
			grandTotal += tx.Nominal
		}

		pdf.CellFormat(25, 8, tx.Tanggal, "1", 0, "C", false, 0, "")
		pdf.CellFormat(70, 8, tx.Deskripsi, "1", 0, "L", false, 0, "")
		pdf.CellFormat(35, 8, tx.Kategori, "1", 0, "L", false, 0, "")
		pdf.CellFormat(30, 8, nominalText, "1", 0, "R", false, 0, "")
		pdf.CellFormat(30, 8, statusLabel, "1", 1, "C", false, 0, "")
	}

	// Grand total row
	pdf.SetFont("Arial", "B", 9.5)
	pdf.CellFormat(130, 8, "Total", "1", 0, "R", false, 0, "")
	pdf.CellFormat(30, 8, formatRupiah(grandTotal), "1", 0, "R", false, 0, "")
	pdf.CellFormat(30, 8, "", "1", 1, "C", false, 0, "")

	w.Header().Set("Content-Type", "application/pdf")
	filename := fmt.Sprintf("laporan_%s_%s_%s.pdf", tipe, strings.ReplaceAll(startDate, "-", ""), strings.ReplaceAll(endDate, "-", ""))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	err = pdf.Output(w)
	if err != nil {
		log.Printf("Error writing PDF output: %v", err)
	}
}

// HandleDownloadXLS generates an Excel spreadsheet
func HandleDownloadXLS(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	tipe, startDate, endDate := parseDownloadParamsRange(r)
	list, err := getReportTransactions(tipe, startDate, endDate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	f := excelize.NewFile()
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("Error closing excelize file: %v", err)
		}
	}()

	sheetName := "Laporan"
	_ = f.SetSheetName("Sheet1", sheetName)

	// Set Headers
	headers := []string{"Tanggal", "Deskripsi", "Kategori", "Tipe", "Nominal", "Status"}
	for i, h := range headers {
		col, _ := excelize.ColumnNumberToName(i + 1)
		_ = f.SetCellValue(sheetName, col+"1", h)
	}

	var grandTotal float64
	for idx, tx := range list {
		row := idx + 2
		_ = f.SetCellValue(sheetName, fmt.Sprintf("A%d", row), tx.Tanggal)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("B%d", row), tx.Deskripsi)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("C%d", row), tx.Kategori)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("D%d", row), tx.Tipe)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("E%d", row), tx.Nominal)

		statusLabel := "Lunas"
		if tx.Status == "planned" {
			statusLabel = "Direncanakan"
		}
		_ = f.SetCellValue(sheetName, fmt.Sprintf("F%d", row), statusLabel)

		if tipe == "all" {
			if tx.Tipe == "income" {
				grandTotal += tx.Nominal
			} else {
				grandTotal -= tx.Nominal
			}
		} else {
			grandTotal += tx.Nominal
		}
	}

	// Add Grand Total
	totalRow := len(list) + 2
	_ = f.SetCellValue(sheetName, fmt.Sprintf("D%d", totalRow), "Total")
	_ = f.SetCellValue(sheetName, fmt.Sprintf("E%d", totalRow), grandTotal)

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	filename := fmt.Sprintf("laporan_%s_%s_%s.xlsx", tipe, strings.ReplaceAll(startDate, "-", ""), strings.ReplaceAll(endDate, "-", ""))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))

	if err := f.Write(w); err != nil {
		log.Printf("Error writing Excel output: %v", err)
	}
}

// HandleDownloadWord generates a Word Document via HTML streaming
func HandleDownloadWord(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	tipe, startDate, endDate := parseDownloadParamsRange(r)
	list, err := getReportTransactions(tipe, startDate, endDate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	periodStr := fmt.Sprintf("%s s/d %s", formatStrDateIndo(startDate), formatStrDateIndo(endDate))
	title := fmt.Sprintf("Laporan Rincian Pengeluaran (%s)", periodStr)
	if tipe == "income" {
		title = fmt.Sprintf("Laporan Rincian Pemasukan (%s)", periodStr)
	} else if tipe == "all" {
		title = fmt.Sprintf("Laporan Rincian Transaksi (%s)", periodStr)
	}

	html := fmt.Sprintf(`<html>
<head>
<meta charset="utf-8">
<style>
body { font-family: Arial, sans-serif; }
h2 { color: #1e293b; }
table { width: 100%%; border-collapse: collapse; margin-top: 20px; }
th, td { border: 1px solid #cbd5e1; padding: 10px; text-align: left; }
th { background-color: #f1f5f9; font-weight: bold; }
.right { text-align: right; }
.center { text-align: center; }
.total { font-weight: bold; background-color: #f8fafc; }
</style>
</head>
<body>
<h2>%s</h2>
<table>
<thead>
<tr>
<th>Tanggal</th>
<th>Deskripsi</th>
<th>Kategori</th>
<th>Tipe</th>
<th>Nominal</th>
<th>Status</th>
</tr>
</thead>
<tbody>`, title)

	var grandTotal float64
	for _, tx := range list {
		statusLabel := "Lunas"
		if tx.Status == "planned" {
			statusLabel = "Direncanakan"
		}

		nominalText := formatRupiah(tx.Nominal)
		if tipe == "all" {
			if tx.Tipe == "income" {
				nominalText = "+" + nominalText
				grandTotal += tx.Nominal
			} else {
				nominalText = "-" + nominalText
				grandTotal -= tx.Nominal
			}
		} else {
			grandTotal += tx.Nominal
		}

		html += fmt.Sprintf(`
<tr>
<td class="center">%s</td>
<td>%s</td>
<td>%s</td>
<td>%s</td>
<td class="right">%s</td>
<td class="center">%s</td>
</tr>`, tx.Tanggal, tx.Deskripsi, tx.Kategori, tx.Tipe, nominalText, statusLabel)
	}

	html += fmt.Sprintf(`
<tr class="total">
<td colspan="4" class="right">Total</td>
<td class="right">%s</td>
<td></td>
</tr>
</tbody>
</table>
</body>
</html>`, formatRupiah(grandTotal))

	w.Header().Set("Content-Type", "application/msword")
	filename := fmt.Sprintf("laporan_%s_%s_%s.doc", tipe, strings.ReplaceAll(startDate, "-", ""), strings.ReplaceAll(endDate, "-", ""))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	w.Write([]byte(html))
}
