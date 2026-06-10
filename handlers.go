package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Helper: Format number to Indonesian Rupiah e.g. Rp 3.200.000
func formatRupiah(val float64) string {
	s := fmt.Sprintf("%.0f", val)
	var result []string
	length := len(s)
	for i, c := range s {
		result = append(result, string(c))
		if (length-i-1)%3 == 0 && i != length-1 {
			result = append(result, ".")
		}
	}
	return "Rp " + strings.Join(result, "")
}

// Helper: Generate unique ID
func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// Helper: Get user help menu state
func GetUserState(userID string) string {
	var state string
	err := DB.QueryRow("SELECT state FROM user_states WHERE user_id = ?", userID).Scan(&state)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		log.Printf("[ERROR] GetUserState failed: %v", err)
		return ""
	}
	return state
}

// Helper: Set user help menu state
func SetUserState(userID string, state string) {
	if state == "" {
		_, _ = DB.Exec("DELETE FROM user_states WHERE user_id = ?", userID)
		return
	}
	_, _ = DB.Exec("INSERT INTO user_states (user_id, state) VALUES (?, ?) ON DUPLICATE KEY UPDATE state = ?", userID, state, state)
}

// Helper: Get salary day setting for a user
func GetSalaryDay(userID string) int {
	var day int
	err := DB.QueryRow("SELECT salary_day FROM user_settings WHERE user_id = ?", userID).Scan(&day)
	if err == sql.ErrNoRows {
		return 1
	}
	if err != nil {
		log.Printf("[ERROR] GetSalaryDay failed: %v", err)
		return 1
	}
	return day
}

// Helper: Set salary day setting for a user
func SetSalaryDay(userID string, day int) {
	_, _ = DB.Exec("INSERT INTO user_settings (user_id, salary_day) VALUES (?, ?) ON DUPLICATE KEY UPDATE salary_day = ?", userID, day, day)
}

// Helper: getSalaryDate determines date for target day capping at month's last day
func getSalaryDate(year int, month int, targetDay int) time.Time {
	tTemp := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	year = tTemp.Year()
	month = int(tTemp.Month())

	lastDay := time.Date(year, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC).Day()
	d := targetDay
	if d > lastDay {
		d = lastDay
	}
	return time.Date(year, time.Month(month), d, 0, 0, 0, 0, time.UTC)
}

// Helper: formatStrDateIndo converts "YYYY-MM-DD" to "DD/MM/YYYY"
func formatStrDateIndo(dateStr string) string {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return dateStr
	}
	return t.Format("02/01/2006")
}

// Helper: formatDateIndo formats time.Time to "DD/MM/YYYY"
func formatDateIndo(t time.Time) string {
	return t.Format("02/01/2006")
}

// Helper: getUserID extracts user ID from query parameters, defaulting to "user_1"
func getUserID(r *http.Request) string {
	if uid := r.URL.Query().Get("user_id"); uid != "" {
		return uid
	}
	return "user_1"
}

var anchorRegex = regexp.MustCompile(`<a\s+[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
var stripTagsRegex = regexp.MustCompile(`<[^>]*>`)

func cleanHTMLForWhatsApp(html string, baseURL string) string {
	// 1. Replace <br> and <br/> with newline
	res := strings.ReplaceAll(html, "<br>", "\n")
	res = strings.ReplaceAll(res, "<br/>", "\n")

	// 2. Replace strong tags with bold asterisks
	res = strings.ReplaceAll(res, "<strong>", "*")
	res = strings.ReplaceAll(res, "</strong>", "*")

	// 3. Process anchor links: <a href="url">text</a> -> text: baseURL + url
	res = anchorRegex.ReplaceAllStringFunc(res, func(anchor string) string {
		matches := anchorRegex.FindStringSubmatch(anchor)
		if len(matches) < 3 {
			return anchor
		}
		url := matches[1]
		text := matches[2]

		// Strip all HTML tags from the inner text (e.g. inner divs, spans)
		text = stripTagsRegex.ReplaceAllString(text, "")
		
		// Clean up spacing
		textLines := strings.Split(text, "\n")
		var cleanedParts []string
		for _, line := range textLines {
			line = strings.TrimSpace(line)
			if line != "" {
				cleanedParts = append(cleanedParts, line)
			}
		}
		text = strings.Join(cleanedParts, " ")
		text = strings.TrimSpace(text)

		// Fix &amp; to &
		url = strings.ReplaceAll(url, "&amp;", "&")

		// If URL is relative, prepend base URL
		if strings.HasPrefix(url, "/") {
			url = baseURL + url
		}

		return fmt.Sprintf("%s: %s", text, url)
	})

	// 4. Strip any remaining HTML tags (like parent divs or helper elements)
	res = stripTagsRegex.ReplaceAllString(res, "")

	// Clean up duplicate spaces or newlines
	res = regexp.MustCompile(`\n{3,}`).ReplaceAllString(res, "\n\n")

	return strings.TrimSpace(res)
}

// Helper: getMonthCycleBounds returns start and end dates for a payroll cycle
func getMonthCycleBounds(year int, month int, salaryDay int) (time.Time, time.Time) {
	if salaryDay <= 1 {
		start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(year, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC)
		return start, end
	}
	prevMonth := month - 1
	prevYear := year
	if prevMonth == 0 {
		prevMonth = 12
		prevYear = year - 1
	}
	start := getSalaryDate(prevYear, prevMonth, salaryDay)

	endDay := salaryDay - 1
	end := getSalaryDate(year, month, endDay)
	return start, end
}

// Helper: Check and generate templates for the current cycle
func checkAndGenerateTemplates(userID string, year int, month int) {
	if DB == nil {
		return
	}

	// 0. Check if templates have already run for this cycle to prevent regeneration if user manually deletes transactions
	var runCount int
	errRun := DB.QueryRow("SELECT COUNT(*) FROM template_runs WHERE user_id = ? AND cycle_year = ? AND cycle_month = ?", userID, year, month).Scan(&runCount)
	if errRun == nil && runCount > 0 {
		return // Already generated for this cycle, respect user deletions!
	}

	// 1. Fetch templates
	rows, err := DB.Query("SELECT tipe, kategori, nominal, deskripsi, target_day, status FROM recurring_templates WHERE user_id = ?", userID)
	if err != nil {
		log.Printf("[ERROR] Failed to query recurring templates: %v", err)
		return
	}
	defer rows.Close()

	type Tmpl struct {
		Tipe      string
		Kategori  string
		Nominal   float64
		Deskripsi string
		TargetDay int
		Status    string
	}
	var templates []Tmpl
	for rows.Next() {
		var t Tmpl
		if err := rows.Scan(&t.Tipe, &t.Kategori, &t.Nominal, &t.Deskripsi, &t.TargetDay, &t.Status); err == nil {
			templates = append(templates, t)
		}
	}

	if len(templates) == 0 {
		return
	}

	salaryDay := GetSalaryDay(userID)
	start, end := getMonthCycleBounds(year, month, salaryDay)

	for _, tmpl := range templates {
		var date time.Time
		if tmpl.TargetDay >= salaryDay {
			date = getSalaryDate(start.Year(), int(start.Month()), tmpl.TargetDay)
		} else {
			date = getSalaryDate(end.Year(), int(end.Month()), tmpl.TargetDay)
		}

		// Check if this transaction already exists for this cycle to prevent duplicate insertion
		var txExists int
		errExists := DB.QueryRow("SELECT COUNT(*) FROM transactions WHERE user_id = ? AND tanggal = ? AND deskripsi = ? AND nominal = ?",
			userID, date.Format("2006-01-02"), tmpl.Deskripsi, tmpl.Nominal).Scan(&txExists)
		if errExists == nil && txExists > 0 {
			continue // Already generated
		}

		txID := generateID()
		var dueDate interface{} = nil
		if tmpl.Status == "planned" {
			dueDate = date.Format("2006-01-02")
		}

		_, err = DB.Exec("INSERT INTO transactions (id, user_id, tanggal, deskripsi, kategori, tipe, nominal, status, due_date, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			txID, userID, date.Format("2006-01-02"), tmpl.Deskripsi, tmpl.Kategori, tmpl.Tipe, tmpl.Nominal, tmpl.Status, dueDate, time.Now())
		if err != nil {
			log.Printf("[ERROR] Failed to insert template transaction: %v", err)
		}
	}

	// Record that templates have run for this cycle
	_, errRunInsert := DB.Exec("INSERT INTO template_runs (user_id, cycle_year, cycle_month) VALUES (?, ?, ?)", userID, year, month)
	if errRunInsert != nil {
		log.Printf("[ERROR] Failed to insert template_runs record: %v", errRunInsert)
	}
}

// Helper: Generate missing template transactions (e.g. for newly added/activated templates mid-cycle)
func generateMissingTemplateTransactions(userID string) {
	if DB == nil {
		return
	}
	rows, err := DB.Query("SELECT tipe, kategori, nominal, deskripsi, target_day, status FROM recurring_templates WHERE user_id = ? AND status = 'planned'", userID)
	if err != nil {
		log.Printf("[ERROR] Failed to query active templates: %v", err)
		return
	}
	defer rows.Close()

	salaryDay := GetSalaryDay(userID)
	start, end := getMonthCycleBounds(time.Now().Year(), int(time.Now().Month()), salaryDay)

	for rows.Next() {
		var tipe, kategori, deskripsi, status string
		var nominal float64
		var targetDay int
		if err := rows.Scan(&tipe, &kategori, &nominal, &deskripsi, &targetDay, &status); err == nil {
			var date time.Time
			if targetDay >= salaryDay {
				date = getSalaryDate(start.Year(), int(start.Month()), targetDay)
			} else {
				date = getSalaryDate(end.Year(), int(end.Month()), targetDay)
			}

			// Check if this transaction already exists for this cycle
			var txExists int
			errExists := DB.QueryRow("SELECT COUNT(*) FROM transactions WHERE user_id = ? AND tanggal = ? AND deskripsi = ? AND nominal = ?",
				userID, date.Format("2006-01-02"), deskripsi, nominal).Scan(&txExists)
			if errExists == nil && txExists == 0 {
				txID := generateID()
				var dueDate interface{} = nil
				if status == "planned" {
					dueDate = date.Format("2006-01-02")
				}

				_, err = DB.Exec("INSERT INTO transactions (id, user_id, tanggal, deskripsi, kategori, tipe, nominal, status, due_date, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
					txID, userID, date.Format("2006-01-02"), deskripsi, kategori, tipe, nominal, status, dueDate, time.Now())
				if err != nil {
					log.Printf("[ERROR] Failed to insert missing template transaction: %v", err)
				}
			}
		}
	}
}

const HelpMainMenuResponse = `🤖 **PANDUAN PENGGUNAAN DOMPETKU**<br><br>Pilih topik bantuan di bawah ini dengan mengetik nomor menu (contoh: ketik **1** atau **2**):<br><br>**1** 📝 Catat Transaksi<br>**2** 📁 Kelola Kategori & Keyword<br>**3** 📊 Laporan & Keuangan<br>**4** 🗑️ Hapus Transaksi<br><br>Ketik **kembali** untuk keluar.`

type HelpOption struct {
	Title     string
	Response  string
	NextState string
}

var HelpMenus = map[string]map[string]HelpOption{
	"HELP_MAIN": {
		"1": {
			Title:     "Catat Transaksi",
			Response:  `📝 **MENU: CATAT TRANSAKSI**<br><br>Pilih sub-topik bantuan dengan mengetik nomor sub-menu:<br><br>**1.1** 💸 Cara Catat Pengeluaran<br>**1.2** 📥 Cara Catat Pemasukan<br>**1.3** 📅 Cara Catat Tagihan (Planned)<br>**1.4** 💳 Cara Melunasi Tagihan<br>**1.5** 🔄 Cara Kelola Template Transaksi<br><br>Ketik **kembali** untuk kembali ke Menu Utama.`,
			NextState: "HELP_CATAT_TRANSAKSI",
		},
		"2": {
			Title:     "Kelola Kategori & Keyword",
			Response:  `📁 **MENU: KELOLA KATEGORI & KEYWORD**<br><br>Pilih sub-topik bantuan dengan mengetik nomor sub-menu:<br><br>**2.1** 📁 Cara Tambah Kategori<br>**2.2** 📋 Cara Lihat Kategori<br>**2.3** 🗑️ Cara Hapus Kategori<br>**2.4** 📝 Cara Tambah Keyword<br>**2.5** 🗑️ Cara Hapus Keyword<br>**2.6** 🔍 Cara Lihat Keyword<br><br>Ketik **kembali** untuk kembali ke Menu Utama.`,
			NextState: "HELP_KELOLA_KATEGORI",
		},
		"3": {
			Title:     "Laporan & Keuangan",
			Response:  `📊 **MENU: LAPORAN & KEUANGAN**<br><br>Pilih sub-topik bantuan dengan mengetik nomor sub-menu:<br><br>**3.1** 💰 Cara Cek Saldo<br>**3.2** 📅 Cara Cek Pengeluaran Hari Ini<br>**3.3** 📅 Cara Cek Pengeluaran Bulan Ini<br>**3.4** 📋 Cara Cek Tagihan Pending<br>**3.5** 🧾 Cara Cek Rincian Pengeluaran<br>**3.6** 🧾 Cara Cek Rincian Pemasukan<br>**3.7** 💾 Download Laporan (PDF, Excel, Word)<br>**3.8** ⚙️ Pengaturan Tanggal Gajian (Siklus Finansial)<br><br>Ketik **kembali** untuk kembali ke Menu Utama.`,
			NextState: "HELP_LAPORAN_KEUANGAN",
		},
		"4": {
			Title:     "Hapus Transaksi",
			Response:  `🗑️ **MENU: HAPUS TRANSAKSI**<br><br>Pilih sub-topik bantuan dengan mengetik nomor sub-menu:<br><br>**4.1** 💸 Hapus Pengeluaran (Hapus data/bulan/nama/semua)<br>**4.2** 📥 Hapus Pemasukan (Hapus data/bulan/nama/semua)<br>**4.3** 🗑️ Cara Hapus Transaksi Terakhir / Tertentu / Nama<br>**4.4** 🔄 Cara Reset Database<br><br>Ketik **kembali** untuk kembali ke Menu Utama.`,
			NextState: "HELP_HAPUS_TRANSAKSI",
		},
	},
	"HELP_CATAT_TRANSAKSI": {
		"1.1": {
			Title:     "Cara Catat Pengeluaran",
			Response:  `💸 **CARA CATAT PENGELUARAN**<br><br>Ketik nominal dan deskripsi pengeluaran secara langsung. Sistem akan mendeteksi nominal dan mengklasifikasikan kategori otomatis.<br><br>**Format**: ` + "`[deskripsi] [nominal]`" + ` atau ` + "`[nominal] [deskripsi]`" + `<br>**Contoh**:<br>• ` + "`beli bakso 20rb`" + `<br>• ` + "`bensin motor 15.000`" + `<br>• ` + "`kopi starbucks 50k`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_CATAT_TRANSAKSI",
		},
		"1.2": {
			Title:     "Cara Catat Pemasukan",
			Response:  `📥 **CARA CATAT PEMASUKAN**<br><br>Ketik deskripsi dan nominal pemasukan. Pastikan pesan mengandung kata kunci pemasukan seperti *gaji, pemasukan, transfer masuk, sampingan, bonus, thr, dll* agar terdeteksi sebagai pemasukan.<br><br>**Format**: ` + "`[deskripsi] [nominal]`" + ` (mengandung kata kunci pemasukan)<br>**Contoh**:<br>• ` + "`gaji bulanan 5 juta`" + `<br>• ` + "`bonus proyek 1.5jt`" + `<br>• ` + "`transfer masuk sampingan 500k`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_CATAT_TRANSAKSI",
		},
		"1.3": {
			Title:     "Cara Catat Tagihan (Planned)",
			Response:  `📅 **CARA CATAT TAGIHAN (PLANNED)**<br><br>Transaksi akan otomatis dicatat sebagai **Tagihan (Planned)** jika mengandung kata kunci tagihan (seperti *tagihan, rencana, nanti, besok, belum*). Sistem juga mentoleransi typo.<br><br>**Format**: ` + "`[deskripsi] [nominal]`" + ` (mengandung kata kunci tagihan)<br>**Contoh**:<br>• ` + "`tagihan listrik 300rb`" + `<br>• ` + "`bayar kontrakan 1.2jt nanti`" + `<br>• ` + "`rencana beli beras 100rb`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_CATAT_TRANSAKSI",
		},
		"1.4": {
			Title:     "Cara Melunasi Tagihan",
			Response:  `💳 **CARA MELUNASI TAGIHAN**<br><br>Untuk mengubah status transaksi dari **Tagihan (Planned)** menjadi **Lunas (Paid)**, gunakan kata kunci bayar/lunas diikuti nama kategori atau deskripsinya.<br><br>**Format**: ` + "`bayar/lunas [nama_kategori / deskripsi]`" + `<br>**Contoh**:<br>• ` + "`bayar listrik`" + `<br>• ` + "`lunas kontrakan`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_CATAT_TRANSAKSI",
		},
		"1.5": {
			Title:     "Cara Kelola Template Transaksi",
			Response:  `🔄 **CARA KELOLA TEMPLATE TRANSAKSI**<br><br>Daftarkan transaksi rutin bulanan Anda (pemasukan & pengeluaran) agar otomatis dicatat setiap awal siklus gajian.<br><br>**1. Tambah Template**<br>Mulai chat dengan **tambah template:** diikuti list transaksi.<br>Format: ` + "`tambah template:\n- [nama] [nominal] tiap [tanggal]`" + `<br>Contoh:<br>` + "`tambah template:\n- gaji bulanan 5jt tiap 28\n- pdam 150rb tiap 5`" + `<br><br>**2. Hapus Template**<br>Ketik perintah hapus diikuti nama template.<br>Format: ` + "`hapus template [nama]`" + `<br>Contoh: ` + "`hapus template pdam`" + `<br><br>**3. Ubah Template**<br>Ketik perintah ubah diikuti detail baru.<br>Format: ` + "`ubah template [nama] jadi [nominal] tiap [tanggal]`" + `<br>Contoh: ` + "`ubah template pdam jadi 200rb tiap 7`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_CATAT_TRANSAKSI",
		},
	},
	"HELP_KELOLA_KATEGORI": {
		"2.1": {
			Title:     "Cara Tambah Kategori",
			Response:  `📁 **CARA TAMBAH KATEGORI**<br><br>Tambahkan kategori transaksi baru langsung melalui pesan.<br><br>**Format**: ` + "`tambah kategori [id] [label] [tipe]`" + `<br>**Contoh**: ` + "`tambah kategori nonton Bioskop & Netflix expense`" + `<br>(ID harus 1 kata lowercase, tipe: ` + "`expense`" + ` atau ` + "`income`" + `)<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_KELOLA_KATEGORI",
		},
		"2.2": {
			Title:     "Cara Lihat Kategori",
			Response:  `📋 **CARA LIHAT KATEGORI**<br><br>Tampilkan semua kategori yang saat ini terdaftar di sistem.<br><br>**Format**: ` + "`daftar kategori`" + ` atau ` + "`lihat kategori`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_KELOLA_KATEGORI",
		},
		"2.3": {
			Title:     "Cara Hapus Kategori",
			Response:  `🗑️ **CARA HAPUS KATEGORI**<br><br>Hapus kategori transaksi berdasarkan ID.<br><br>**Format**: ` + "`hapus kategori [id]`" + `<br>**Contoh**: ` + "`hapus kategori nonton`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_KELOLA_KATEGORI",
		},
		"2.4": {
			Title:     "Cara Tambah Keyword",
			Response:  `📝 **CARA TAMBAH KEYWORD**<br><br>Tambahkan satu atau beberapa kata kunci pencocokan untuk suatu kategori.<br><br>**Format**: ` + "`tambah keyword [kategori_id] [keyword1, keyword2, ...]`" + `<br>**Contoh**: ` + "`tambah keyword nonton netflix, bioskop, hbo`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_KELOLA_KATEGORI",
		},
		"2.5": {
			Title:     "Cara Hapus Keyword",
			Response:  `🗑️ **CARA HAPUS KEYWORD**<br><br>Hapus beberapa kata kunci dari kategori tertentu.<br><br>**Format**: ` + "`hapus keyword [kategori_id] [keyword1, keyword2, ...]`" + `<br>**Contoh**: ` + "`hapus keyword nonton hbo`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_KELOLA_KATEGORI",
		},
		"2.6": {
			Title:     "Cara Lihat Keyword",
			Response:  `🔍 **CARA LIHAT KEYWORD**<br><br>Tampilkan semua kata kunci dasar yang terdaftar di bawah kategori tertentu.<br><br>**Format**: ` + "`daftar keyword [kategori_id]`" + `<br>**Contoh**: ` + "`daftar keyword nonton`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_KELOLA_KATEGORI",
		},
	},
	"HELP_LAPORAN_KEUANGAN": {
		"3.1": {
			Title:     "Cara Cek Saldo",
			Response:  `💰 **CARA CEK SALDO**<br><br>Tanyakan saldo saat ini, total tagihan tertunda, dan sisa saldo aman.<br><br>**Format/Kata kunci**: *saldo, uang saya, sisa uang, duit*<br>**Contoh**: ` + "`saldo saya berapa?`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_LAPORAN_KEUANGAN",
		},
		"3.2": {
			Title:     "Cara Cek Pengeluaran Hari Ini",
			Response:  `📅 **CARA CEK PENGELUARAN HARI INI**<br><br>Tampilkan daftar pengeluaran yang telah dibayar pada hari ini beserta total nominalnya.<br><br>**Format/Kata kunci**: *pengeluaran hari ini, hari ini, hariini*<br>**Contoh**: ` + "`pengeluaran hari ini`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_LAPORAN_KEUANGAN",
		},
		"3.3": {
			Title:     "Cara Cek Pengeluaran Bulan Ini",
			Response:  `📅 **CARA CEK PENGELUARAN BULAN INI**<br><br>Tampilkan total nominal pengeluaran lunas sepanjang bulan ini.<br><br>**Format/Kata kunci**: *bulan ini, habis berapa, pengeluaran bulan*<br>**Contoh**: ` + "`bulan ini habis berapa`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_LAPORAN_KEUANGAN",
		},
		"3.4": {
			Title:     "Cara Cek Tagihan Pending",
			Response:  `📋 **CARA CEK TAGIHAN PENDING**<br><br>Tampilkan daftar seluruh tagihan (planned) yang belum dilunasi.<br><br>**Format/Kata kunci**: *tagihan, belum bayar, belum dibayar, rencana*<br>**Contoh**: ` + "`tagihan apa saja?`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_LAPORAN_KEUANGAN",
		},
		"3.5": {
			Title:     "Cara Cek Rincian Pengeluaran",
			Response:  `🧾 **CARA CEK RINCIAN PENGELUARAN**<br><br>Tampilkan daftar rincian seluruh pengeluaran bulanan.<br><br>**Format/Kata kunci**: *list pengeluaran, rincian pengeluaran, daftar pengeluaran [bulan] [tahun]* (atau dengan tanggal kustom: *dari [tanggal] sampai [tanggal]*)<br>**Contoh**:<br>• ` + "`list pengeluaran bulan ini`" + `<br>• ` + "`rincian pengeluaran dari 25 mei sd 24 juni`" + `<br>• ` + "`daftar pengeluaran 25 sd 28 juni 2026`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_LAPORAN_KEUANGAN",
		},
		"3.6": {
			Title:     "Cara Cek Rincian Pemasukan",
			Response:  `🧾 **CARA CEK RINCIAN PEMASUKAN**<br><br>Tampilkan daftar rincian seluruh pemasukan bulanan.<br><br>**Format/Kata kunci**: *list pemasukan, rincian pemasukan, daftar pemasukan [bulan] [tahun]* (atau dengan tanggal kustom: *dari [tanggal] sampai [tanggal]*)<br>**Contoh**:<br>• ` + "`list pemasukan bulan ini`" + `<br>• ` + "`rincian pemasukan dari 25 mei sampai 24 juni`" + `<br>• ` + "`daftar pemasukan 25-28 juni 2026`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_LAPORAN_KEUANGAN",
		},
		"3.7": {
			Title:     "Download Laporan",
			Response:  `💾 **DOWNLOAD LAPORAN**<br><br>Unduh laporan transaksi bulan ini secara instan:<br><br>**Laporan Pengeluaran bulan ini**:<br>• <a href="/api/download/pdf?tipe=expense" download class="text-[#00a884] hover:underline">📄 Download PDF</a><br>• <a href="/api/download/xls?tipe=expense" download class="text-[#00a884] hover:underline">📊 Download Excel</a><br>• <a href="/api/download/word?tipe=expense" download class="text-[#00a884] hover:underline">📝 Download Word</a><br><br>**Laporan Pemasukan bulan ini**:<br>• <a href="/api/download/pdf?tipe=income" download class="text-[#00a884] hover:underline">📄 Download PDF</a><br>• <a href="/api/download/xls?tipe=income" download class="text-[#00a884] hover:underline">📊 Download Excel</a><br>• <a href="/api/download/word?tipe=income" download class="text-[#00a884] hover:underline">📝 Download Word</a><br><br>**Laporan Semua Transaksi bulan ini**:<br>• <a href="/api/download/pdf?tipe=all" download class="text-[#00a884] hover:underline">📄 Download PDF</a><br>• <a href="/api/download/xls?tipe=all" download class="text-[#00a884] hover:underline">📊 Download Excel</a><br>• <a href="/api/download/word?tipe=all" download class="text-[#00a884] hover:underline">📝 Download Word</a><br><br>Atau Anda juga bisa memintanya lewat chat (termasuk rentang tanggal kustom), contoh:<br>• ` + "`list pengeluaran pdf dari 25 mei sd 24 juni`" + `<br>• ` + "`rincian pemasukan berupa excel`" + `<br>• ` + "`daftar transaksi word`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_LAPORAN_KEUANGAN",
		},
		"3.8": {
			Title:     "Pengaturan Tanggal Gajian",
			Response:  `⚙️ **PENGATURAN TANGGAL GAJIAN**<br><br>Atur tanggal gajian Anda untuk menyesuaikan siklus laporan bulanan secara otomatis.<br><br>**Format/Kata kunci**: *set tanggal gajian [tanggal]*, *gajian saya tanggal [tanggal]*<br>**Contoh**:<br>• ` + "`set tanggal gajian 25`" + `<br>• ` + "`gajian saya tanggal 25`" + `<br><br>Setelah diset, rekapan bulanan Anda (misal: "bulan ini") akan otomatis dihitung dari tanggal gajian bulan lalu sampai sehari sebelum tanggal gajian bulan ini.<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_LAPORAN_KEUANGAN",
		},
	},
	"HELP_HAPUS_TRANSAKSI": {
		"4.3": {
			Title:     "Cara Hapus Transaksi Terakhir / Tertentu / Nama",
			Response:  `🗑️ **CARA HAPUS TRANSAKSI TERAKHIR / TERTENTU / NAMA**<br><br>**1. Hapus Transaksi Terakhir**<br>Hapus transaksi yang baru saja Anda masukkan.<br>**Format/Kata kunci**: *hapus, batal, cancel, delete* (tanpa deskripsi)<br>**Contoh**: ` + "`batal`" + ` atau ` + "`hapus transaksi terakhir`" + `<br><br>**2. Hapus Transaksi Tertentu / Nama**<br>Hapus transaksi berdasarkan kata kunci pada deskripsi/kategori.<br>**Format**: ` + "`hapus/batal [kata_kunci]`" + ` atau ` + "`hapus pengeluaran/pemasukan [kata_kunci]`" + `<br>**Contoh**:<br>• ` + "`hapus bensin`" + `<br>• ` + "`hapus pengeluaran tagihan listrik`" + `<br>• ` + "`hapus pemasukan gaji`" + `<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_HAPUS_TRANSAKSI",
		},
		"4.4": {
			Title:     "Cara Reset Database",
			Response:  `🔄 **CARA RESET DATABASE**<br><br>Untuk membersihkan seluruh transaksi dan riwayat chat serta mengembalikannya ke data demo, gunakan tombol **Reset & Demo Data** di bagian kanan atas dashboard web.<br><br>Ketik **kembali** untuk kembali ke menu sebelumnya, atau **help** untuk ke Menu Utama.`,
			NextState: "HELP_HAPUS_TRANSAKSI",
		},
	},
}

// GetChatHistory returns messages
func GetChatHistory(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	userID := getUserID(r)
	rows, err := DB.Query("SELECT id, sender, message, created_at FROM chat_history WHERE user_id = ? ORDER BY id ASC", userID)
	if err != nil {
		log.Printf("[ERROR] GetChatHistory SQL query failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	history := []ChatMessage{}
	for rows.Next() {
		var msg ChatMessage
		if err := rows.Scan(&msg.ID, &msg.Sender, &msg.Message, &msg.CreatedAt); err != nil {
			log.Printf("[ERROR] GetChatHistory rows scan failed: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		history = append(history, msg)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history)
}

// GetTransactions retrieves filters
func GetTransactions(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}
	userID := getUserID(r)
	checkAndGenerateTemplates(userID, time.Now().Year(), int(time.Now().Month()))

	tipeFilter := r.URL.Query().Get("type")
	statusFilter := r.URL.Query().Get("status")

	query := "SELECT id, user_id, tanggal, deskripsi, kategori, tipe, nominal, status, due_date, created_at FROM transactions"
	var args []interface{}
	var conditions []string

	conditions = append(conditions, "user_id = ?")
	args = append(args, userID)

	if tipeFilter != "" && tipeFilter != "all" {
		conditions = append(conditions, "tipe = ?")
		args = append(args, tipeFilter)
	}
	if statusFilter != "" && statusFilter != "all" {
		conditions = append(conditions, "status = ?")
		args = append(args, statusFilter)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY tanggal DESC, created_at DESC"

	rows, err := DB.Query(query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []Transaction{}
	for rows.Next() {
		var tx Transaction
		var dueDate sql.NullString
		if err := rows.Scan(&tx.ID, &tx.UserID, &tx.Tanggal, &tx.Deskripsi, &tx.Kategori, &tx.Tipe, &tx.Nominal, &tx.Status, &dueDate, &tx.CreatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if dueDate.Valid {
			tx.DueDate = &dueDate.String
		}
		list = append(list, tx)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// CreateTransaction inserts a transaction manually
func CreateTransaction(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	var tx Transaction
	if err := json.NewDecoder(r.Body).Decode(&tx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	tx.ID = generateID()
	tx.UserID = getUserID(r)
	tx.CreatedAt = time.Now()

	var dueDateVal interface{}
	if tx.DueDate != nil && *tx.DueDate != "" {
		dueDateVal = *tx.DueDate
	} else {
		dueDateVal = nil
	}

	_, err := DB.Exec("INSERT INTO transactions (id, user_id, tanggal, deskripsi, kategori, tipe, nominal, status, due_date, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		tx.ID, tx.UserID, tx.Tanggal, tx.Deskripsi, tx.Kategori, tx.Tipe, tx.Nominal, tx.Status, dueDateVal, tx.CreatedAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Fetch category label
	catLabel := tx.Kategori
	_ = DB.QueryRow("SELECT label FROM categories WHERE id = ?", tx.Kategori).Scan(&catLabel)

	// Send notification
	typeLabel := "Pemasukan"
	if tx.Tipe == "expense" {
		typeLabel = "Pengeluaran"
	}
	statusLabel := "Lunas (Paid)"
	if tx.Status == "planned" {
		statusLabel = "Direncanakan (Planned)"
	}
	notifMsg := fmt.Sprintf("📢 **Notifikasi Dashboard**: Transaksi baru berhasil ditambahkan melalui dashboard!<br><br>**%s**: %s<br>**Nominal**: %s<br>**Kategori**: %s<br>**Status**: %s",
		typeLabel, tx.Deskripsi, formatRupiah(tx.Nominal), catLabel, statusLabel)
	_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", tx.UserID, "bot", notifMsg)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(tx)
}

// UpdateTransactionStatus sets status to paid
func UpdateTransactionStatus(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/transactions/")
	id = strings.TrimSuffix(id, "/status")

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	userID := getUserID(r)

	var deskripsi string
	var nominal float64
	var txUserID string
	errRow := DB.QueryRow("SELECT deskripsi, nominal, user_id FROM transactions WHERE id = ? AND user_id = ?", id, userID).Scan(&deskripsi, &nominal, &txUserID)
	if errRow == sql.ErrNoRows {
		http.Error(w, "Transaction not found or unauthorized", http.StatusNotFound)
		return
	}

	_, err := DB.Exec("UPDATE transactions SET status = ? WHERE id = ? AND user_id = ?", body.Status, id, userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if deskripsi != "" {
		statusLabel := "Lunas (Paid)"
		if body.Status == "planned" {
			statusLabel = "Direncanakan (Planned)"
		}
		notifMsg := fmt.Sprintf("📢 **Notifikasi Dashboard**: Status transaksi **%s** (%s) telah diperbarui menjadi **%s** melalui dashboard! 💳",
			deskripsi, formatRupiah(nominal), statusLabel)
		_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", notifMsg)
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Status updated successfully"})
}

// DeleteTransaction removes record
func DeleteTransaction(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/transactions/")

	userID := getUserID(r)

	var deskripsi string
	var nominal float64
	var txUserID string
	errRow := DB.QueryRow("SELECT deskripsi, nominal, user_id FROM transactions WHERE id = ? AND user_id = ?", id, userID).Scan(&deskripsi, &nominal, &txUserID)
	if errRow == sql.ErrNoRows {
		http.Error(w, "Transaction not found or unauthorized", http.StatusNotFound)
		return
	}

	_, err := DB.Exec("DELETE FROM transactions WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if deskripsi != "" {
		notifMsg := fmt.Sprintf("📢 **Notifikasi Dashboard**: Transaksi **%s** (%s) telah dihapus dari sistem melalui dashboard! 🗑️",
			deskripsi, formatRupiah(nominal))
		_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", notifMsg)
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Transaction deleted successfully"})
}

// GetFinancials aggregates KPIs and charts
func GetFinancials(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}
	userID := getUserID(r)
	checkAndGenerateTemplates(userID, time.Now().Year(), int(time.Now().Month()))

	// 1. Calculate Saldo & Tagihan
	var paidIncome, paidExpense, plannedExpense float64
	var tagihanCount int

	err := DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'income' AND status = 'paid' AND user_id = ?", userID).Scan(&paidIncome)
	if err != nil {
		log.Printf("[ERROR] GetFinancials paidIncome query failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'expense' AND status = 'paid' AND user_id = ?", userID).Scan(&paidExpense)
	if err != nil {
		log.Printf("[ERROR] GetFinancials paidExpense query failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'expense' AND status = 'planned' AND user_id = ?", userID).Scan(&plannedExpense)
	if err != nil {
		log.Printf("[ERROR] GetFinancials plannedExpense query failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = DB.QueryRow("SELECT COUNT(*) FROM transactions WHERE tipe = 'expense' AND status = 'planned' AND user_id = ?", userID).Scan(&tagihanCount)
	if err != nil {
		log.Printf("[ERROR] GetFinancials tagihanCount query failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	saldo := paidIncome - paidExpense
	sisaAman := saldo - plannedExpense

	summary := KPISummary{
		Saldo:            saldo,
		TotalTagihan:     plannedExpense,
		TagihanCount:     tagihanCount,
		SisaAman:         sisaAman,
		TotalPengeluaran: paidExpense,
	}

	// 2. Fetch Chart details (grouped by category for successful expenses this month/cycle)
	currentMonth := int(time.Now().Month())
	currentYear := time.Now().Year()
	salaryDay := GetSalaryDay(userID)
	start, end := getMonthCycleBounds(currentYear, currentMonth, salaryDay)
	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")

	chartQuery := `
		SELECT kategori, SUM(nominal) 
		FROM transactions 
		WHERE tipe = 'expense' AND status = 'paid' AND tanggal >= ? AND tanggal <= ? AND user_id = ?
		GROUP BY kategori`

	rows, err := DB.Query(chartQuery, startStr, endStr, userID)
	if err != nil {
		log.Printf("[ERROR] GetFinancials chartQuery failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	// Load categories map to lookup labels dynamically
	catLabels := map[string]string{}
	catRows, errCat := DB.Query("SELECT id, label FROM categories WHERE tipe = 'expense'")
	if errCat == nil {
		defer catRows.Close()
		for catRows.Next() {
			var cid, clabel string
			if errScan := catRows.Scan(&cid, &clabel); errScan == nil {
				catLabels[cid] = clabel
			}
		}
	}

	chartData := []ChartDataPoint{}
	for rows.Next() {
		var cat string
		var sum float64
		if err := rows.Scan(&cat, &sum); err != nil {
			log.Printf("[ERROR] GetFinancials chart rows scan failed: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		label, exists := catLabels[cat]
		if !exists {
			label = cat
			if len(label) > 0 {
				label = strings.ToUpper(label[0:1]) + label[1:]
			}
		}
		chartData = append(chartData, ChartDataPoint{Label: label, Value: sum})
	}

	payload := map[string]interface{}{
		"kpis":  summary,
		"chart": chartData,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
}

// GenerateReport returns text summaries
func GenerateReport(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	userID := getUserID(r)
	reportType := strings.TrimPrefix(r.URL.Path, "/api/reports/")
	todayStr := time.Now().Format("2006-01-02")
	currentMonth := int(time.Now().Month())
	currentYear := time.Now().Year()

	var title, body string

	if reportType == "harian" {
		title = fmt.Sprintf("Laporan Keuangan Harian (%s)", todayStr)

		var totalExpense float64
		err := DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'expense' AND status = 'paid' AND tanggal = ? AND user_id = ?", todayStr, userID).Scan(&totalExpense)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		rows, err := DB.Query("SELECT kategori, SUM(nominal) FROM transactions WHERE tipe = 'expense' AND status = 'paid' AND tanggal = ? AND user_id = ? GROUP BY kategori", todayStr, userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var catLines []string
		for rows.Next() {
			var cat string
			var sum float64
			_ = rows.Scan(&cat, &sum)
			catLines = append(catLines, fmt.Sprintf("- %s: %s", cat, formatRupiah(sum)))
		}

		catDetails := strings.Join(catLines, "\n")
		if len(catLines) == 0 {
			catDetails = "- Belum ada pengeluaran harian."
		}

		body = fmt.Sprintf(`=== LAPORAN HARIAN DOMPETKU ===
Tanggal: %s

* Total Pengeluaran: %s

* Rincian Kategori:
%s
==============================`, todayStr, formatRupiah(totalExpense), catDetails)

	} else if reportType == "bulanan" {
		monthNames := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
		salaryDay := GetSalaryDay(userID)
		start, end := getMonthCycleBounds(currentYear, currentMonth, salaryDay)
		startStr := start.Format("2006-01-02")
		endStr := end.Format("2006-01-02")

		monthLabel := fmt.Sprintf("%s %d (%s s/d %s)", monthNames[currentMonth], currentYear, formatDateIndo(start), formatDateIndo(end))
		title = "Laporan Keuangan Bulanan - " + monthLabel

		var monthIncome, monthExpense, monthPlanned float64
		_ = DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'income' AND status = 'paid' AND tanggal >= ? AND tanggal <= ? AND user_id = ?", startStr, endStr, userID).Scan(&monthIncome)
		_ = DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'expense' AND status = 'paid' AND tanggal >= ? AND tanggal <= ? AND user_id = ?", startStr, endStr, userID).Scan(&monthExpense)
		_ = DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'expense' AND status = 'planned' AND tanggal >= ? AND tanggal <= ? AND user_id = ?", startStr, endStr, userID).Scan(&monthPlanned)

		// Top category calculation
		var topCategory string
		var topSum float64
		err := DB.QueryRow(`
			SELECT kategori, SUM(nominal) as total 
			FROM transactions 
			WHERE tipe = 'expense' AND status = 'paid' AND tanggal >= ? AND tanggal <= ? AND user_id = ?
			GROUP BY kategori 
			ORDER BY total DESC LIMIT 1`, startStr, endStr, userID).Scan(&topCategory, &topSum)

		if err != nil && err != sql.ErrNoRows {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		topCatLabel := "Tidak ada"
		if topCategory != "" {
			topCatLabel = fmt.Sprintf("%s (%s)", topCategory, formatRupiah(topSum))
		}

		body = fmt.Sprintf(`=== LAPORAN BULANAN DOMPETKU ===
Periode: %s

* Total Pemasukan: %s
* Total Pengeluaran: %s
* Tagihan Tertunda: %s

* Kategori Terbesar:
  %s

* Status Saldo Akhir:
  %s
===============================`, monthLabel, formatRupiah(monthIncome), formatRupiah(monthExpense), formatRupiah(monthPlanned), topCatLabel, formatRupiah(monthIncome-monthExpense))
	}

	payload := map[string]string{
		"title":   title,
		"content": body,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
}

// ProcessChat accepts user input, executes NLP parser, mutates database, returns response
func ProcessChat(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	var body struct {
		Message string `json:"message"`
		UserID  string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	userID := body.UserID
	if userID == "" {
		userID = getUserID(r)
	}

	// Auto-initialize new users (with demo transactions and a welcome message)
	var txCount int
	errTxCheck := DB.QueryRow("SELECT COUNT(*) FROM transactions WHERE user_id = ?", userID).Scan(&txCount)
	var chatCount int
	errChatCheck := DB.QueryRow("SELECT COUNT(*) FROM chat_history WHERE user_id = ?", userID).Scan(&chatCount)
	if (errTxCheck == nil && txCount == 0) && (errChatCheck == nil && chatCount == 0) {
		seedMockDataForUser(userID)
	}

	checkAndGenerateTemplates(userID, time.Now().Year(), int(time.Now().Month()))

	userMsg := strings.TrimSpace(body.Message)
	if userMsg == "" {
		http.Error(w, "Message is empty", http.StatusBadRequest)
		return
	}

	// 1. Save User message to history
	_, err := DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "user", userMsg)
	if err != nil {
		log.Printf("Error saving user chat: %v", err)
	}

	// 2. Process via NLP engine
	analysis := ProcessUserInput(userMsg)
	replyText := ""
	isStateProcessed := false

	if analysis.Intent == "HELP_TRIGGER" {
		SetUserState(userID, "HELP_MAIN")
		replyText = HelpMainMenuResponse
		isStateProcessed = true
	} else if analysis.Intent == "INTRO_TRIGGER" {
		SetUserState(userID, "INTRO_MAIN")
		var content string
		err := DB.QueryRow("SELECT content FROM system_info WHERE topic_key = 'INTRO_MAIN'").Scan(&content)
		if err != nil {
			log.Printf("DB Error fetching INTRO_MAIN: %v", err)
			replyText = "Gagal memuat informasi perkenalan. 🥺"
		} else {
			replyText = content
		}
		isStateProcessed = true
	} else {
		currentState := GetUserState(userID)
		// Break out of current state if the user typed a specific command intent
		if currentState != "" && (analysis.Intent == "TRIGGER_RESET_DATA" || analysis.Intent == "DELETE_TEMPLATE" || analysis.Intent == "UPDATE_TEMPLATE" || analysis.Intent == "CREATE_TEMPLATES" || analysis.Intent == "QUERY_SALARY_DAY" || analysis.Intent == "DELETE_TRANSAKSI" || analysis.Intent == "HELP_TRIGGER") {
			SetUserState(userID, "")
			currentState = ""
		}

		if currentState != "" {
			cleanedMsg := strings.ToLower(strings.TrimSpace(userMsg))

			if currentState == "CONFIRM_DELETE_EXPENSE_MAIN" {
				if cleanedMsg == "5" || cleanedMsg == "kembali" || cleanedMsg == "batal" {
					SetUserState(userID, "")
					replyText = "Penghapusan pengeluaran berhasil dibatalkan. Riwayat pengeluaran Anda tetap aman! 😌"
				} else if cleanedMsg == "1" {
					_, err := DB.Exec("DELETE FROM transactions WHERE user_id = ?", userID)
					if err != nil {
						log.Printf("DB Error deleting all transactions: %v", err)
						replyText = "Gagal menghapus data transaksi. 🥺"
					} else {
						SetUserState(userID, "")
						replyText = "⚠️ **PEMBERITAHUAN**: Seluruh data pemasukan dan pengeluaran Anda berhasil dihapus dari sistem! 🗑️"
						_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", "📢 **Notifikasi Sistem**: Seluruh data transaksi telah dihapus oleh pengguna.")
					}
				} else if cleanedMsg == "2" {
					currentMonth := int(time.Now().Month())
					currentYear := time.Now().Year()
					salaryDay := GetSalaryDay(userID)
					start, end := getMonthCycleBounds(currentYear, currentMonth, salaryDay)
					startStr := start.Format("2006-01-02")
					endStr := end.Format("2006-01-02")

					_, err := DB.Exec("DELETE FROM transactions WHERE tipe = 'expense' AND tanggal >= ? AND tanggal <= ? AND user_id = ?", startStr, endStr, userID)
					if err != nil {
						log.Printf("DB Error deleting month expenses: %v", err)
						replyText = "Gagal menghapus data pengeluaran bulan ini. 🥺"
					} else {
						SetUserState(userID, "")
						periodLabel := fmt.Sprintf("%02d/%02d/%d s/d %02d/%02d/%d", start.Day(), start.Month(), start.Year(), end.Day(), end.Month(), end.Year())
						replyText = fmt.Sprintf("Data pengeluaran untuk periode bulan ini (**%s**) berhasil dihapus! 🗑️", periodLabel)
						_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", fmt.Sprintf("📢 **Notifikasi Sistem**: Data pengeluaran untuk periode %s telah dihapus oleh pengguna.", periodLabel))
					}
				} else if cleanedMsg == "3" {
					SetUserState(userID, "CONFIRM_DELETE_EXPENSE_BY_NAME")
					replyText = "Silakan ketik nama/deskripsi pengeluaran yang ingin Anda hapus.<br>Contoh: **bensin** atau **tagihan listrik**.<br><br>Ketik **kembali** untuk membatalkan."
				} else if cleanedMsg == "4" {
					SetUserState(userID, "CONFIRM_DELETE_EXPENSE_MONTH")
					replyText = "Silakan ketik nama bulan dan tahun yang ingin Anda hapus data pengeluarannya.<br>Contoh: **Juni 2026** atau **Mei 2026**.<br><br>Ketik **kembali** untuk membatalkan."
				} else {
					replyText = "Pilihan tidak valid. Silakan pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Pengeluaran Bulan Ini<br>**3** Hapus Pengeluaran Berdasarkan Nama<br>**4** Hapus Pengeluaran Bulan Tertentu<br>**5** Batal (Kembali)"
				}
				isStateProcessed = true
			} else if currentState == "CONFIRM_DELETE_EXPENSE_BY_NAME" {
				if cleanedMsg == "kembali" || cleanedMsg == "batal" {
					SetUserState(userID, "CONFIRM_DELETE_EXPENSE_MAIN")
					now := time.Now()
					monthNamesIndo := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
					currentMonthName := monthNamesIndo[int(now.Month())]
					replyText = fmt.Sprintf(`⚠️ **KONFIRMASI PENGHAPUSAN PENGELUARAN**<br><br>Apakah Anda yakin ingin menghapus data pengeluaran Anda? Tindakan ini tidak dapat dibatalkan.<br><br>Pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Pengeluaran Bulan Ini (%s %d)<br>**3** Hapus Pengeluaran Berdasarkan Nama<br>**4** Hapus Pengeluaran Bulan Tertentu<br>**5** Batal (Kembali)`, currentMonthName, now.Year())
				} else {
					var id, deskripsi string
					var nominal float64
					likeKeyword := "%" + cleanedMsg + "%"
					err := DB.QueryRow("SELECT id, deskripsi, nominal FROM transactions WHERE (deskripsi LIKE ? OR kategori LIKE ?) AND tipe = 'expense' AND user_id = ? ORDER BY created_at DESC, id DESC LIMIT 1", likeKeyword, likeKeyword, userID).Scan(&id, &deskripsi, &nominal)
					if err == sql.ErrNoRows {
						replyText = fmt.Sprintf("Tidak ditemukan pengeluaran yang cocok dengan kata kunci **\"%s\"**. 🤔<br>Silakan ketik nama/deskripsi pengeluaran lain, atau ketik **kembali**.", cleanedMsg)
					} else if err != nil {
						log.Printf("DB Error finding expense: %v", err)
						replyText = "Terjadi kesalahan saat mencari data pengeluaran. 🥺"
					} else {
						SetUserState(userID, "CONFIRM_DELETE_LAST_"+id)
						replyText = fmt.Sprintf("Apakah Anda yakin ingin menghapus pengeluaran **%s** sebesar **%s**? 🤔<br><br>**1** Ya, Hapus<br>**2** Batal (Kembali)", deskripsi, formatRupiah(nominal))
					}
				}
				isStateProcessed = true
			} else if currentState == "CONFIRM_DELETE_EXPENSE_MONTH" {
				if cleanedMsg == "kembali" || cleanedMsg == "batal" {
					SetUserState(userID, "CONFIRM_DELETE_EXPENSE_MAIN")
					now := time.Now()
					monthNamesIndo := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
					currentMonthName := monthNamesIndo[int(now.Month())]
					replyText = fmt.Sprintf(`⚠️ **KONFIRMASI PENGHAPUSAN PENGELUARAN**<br><br>Apakah Anda yakin ingin menghapus data pengeluaran Anda? Tindakan ini tidak dapat dibatalkan.<br><br>Pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Pengeluaran Bulan Ini (%s %d)<br>**3** Hapus Pengeluaran Berdasarkan Nama<br>**4** Hapus Pengeluaran Bulan Tertentu<br>**5** Batal (Kembali)`, currentMonthName, now.Year())
				} else {
					month, year := parseMonthAndYear(cleanedMsg)
					hasMonthWord := false
					for name := range indoMonths {
						if strings.Contains(cleanedMsg, name) {
							hasMonthWord = true
							break
						}
					}
					if !hasMonthWord && !strings.Contains(cleanedMsg, "bulan lalu") {
						replyText = "Format bulan tidak dikenali. Silakan ketik nama bulan dan tahun dengan benar (Contoh: **Mei 2026**), atau ketik **kembali**."
					} else {
						salaryDay := GetSalaryDay(userID)
						start, end := getMonthCycleBounds(year, month, salaryDay)
						startStr := start.Format("2006-01-02")
						endStr := end.Format("2006-01-02")

						_, err := DB.Exec("DELETE FROM transactions WHERE tipe = 'expense' AND tanggal >= ? AND tanggal <= ? AND user_id = ?", startStr, endStr, userID)
						if err != nil {
							log.Printf("DB Error deleting custom month expenses: %v", err)
							replyText = "Gagal menghapus data pengeluaran untuk periode tersebut. 🥺"
						} else {
							SetUserState(userID, "")
							monthNamesIndo := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
							monthName := monthNamesIndo[month]
							periodLabel := fmt.Sprintf("%02d/%02d/%d s/d %02d/%02d/%d", start.Day(), start.Month(), start.Year(), end.Day(), end.Month(), end.Year())
							replyText = fmt.Sprintf("Data pengeluaran untuk periode **%s %d** (%s) berhasil dihapus! 🗑️", monthName, year, periodLabel)
							_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", fmt.Sprintf("📢 **Notifikasi Sistem**: Data pengeluaran untuk periode %s %d (%s) telah dihapus oleh pengguna.", monthName, year, periodLabel))
						}
					}
				}
				isStateProcessed = true
			} else if currentState == "CONFIRM_DELETE_INCOME_MAIN" {
				if cleanedMsg == "5" || cleanedMsg == "kembali" || cleanedMsg == "batal" {
					SetUserState(userID, "")
					replyText = "Penghapusan pemasukan berhasil dibatalkan. Riwayat pemasukan Anda tetap aman! 😌"
				} else if cleanedMsg == "1" {
					_, err := DB.Exec("DELETE FROM transactions WHERE user_id = ?", userID)
					if err != nil {
						log.Printf("DB Error deleting all transactions: %v", err)
						replyText = "Gagal menghapus data transaksi. 🥺"
					} else {
						SetUserState(userID, "")
						replyText = "⚠️ **PEMBERITAHUAN**: Seluruh data pemasukan dan pengeluaran Anda berhasil dihapus dari sistem! 🗑️"
						_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", "📢 **Notifikasi Sistem**: Seluruh data transaksi telah dihapus oleh pengguna.")
					}
				} else if cleanedMsg == "2" {
					currentMonth := int(time.Now().Month())
					currentYear := time.Now().Year()
					salaryDay := GetSalaryDay(userID)
					start, end := getMonthCycleBounds(currentYear, currentMonth, salaryDay)
					startStr := start.Format("2006-01-02")
					endStr := end.Format("2006-01-02")

					_, err := DB.Exec("DELETE FROM transactions WHERE tipe = 'income' AND tanggal >= ? AND tanggal <= ? AND user_id = ?", startStr, endStr, userID)
					if err != nil {
						log.Printf("DB Error deleting month incomes: %v", err)
						replyText = "Gagal menghapus data pemasukan bulan ini. 🥺"
					} else {
						SetUserState(userID, "")
						periodLabel := fmt.Sprintf("%02d/%02d/%d s/d %02d/%02d/%d", start.Day(), start.Month(), start.Year(), end.Day(), end.Month(), end.Year())
						replyText = fmt.Sprintf("Data pemasukan untuk periode bulan ini (**%s**) berhasil dihapus! 🗑️", periodLabel)
						_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", fmt.Sprintf("📢 **Notifikasi Sistem**: Data pemasukan untuk periode %s telah dihapus oleh pengguna.", periodLabel))
					}
				} else if cleanedMsg == "3" {
					SetUserState(userID, "CONFIRM_DELETE_INCOME_BY_NAME")
					replyText = "Silakan ketik nama/deskripsi pemasukan yang ingin Anda hapus.<br>Contoh: **gaji** atau **bonus**.<br><br>Ketik **kembali** untuk membatalkan."
				} else if cleanedMsg == "4" {
					SetUserState(userID, "CONFIRM_DELETE_INCOME_MONTH")
					replyText = "Silakan ketik nama bulan dan tahun yang ingin Anda hapus data pemasukannya.<br>Contoh: **Juni 2026** atau **Mei 2026**.<br><br>Ketik **kembali** untuk membatalkan."
				} else {
					replyText = "Pilihan tidak valid. Silakan pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Pemasukan Bulan Ini<br>**3** Hapus Pemasukan Berdasarkan Nama<br>**4** Hapus Pemasukan Bulan Tertentu<br>**5** Batal (Kembali)"
				}
				isStateProcessed = true
			} else if currentState == "CONFIRM_DELETE_INCOME_BY_NAME" {
				if cleanedMsg == "kembali" || cleanedMsg == "batal" {
					SetUserState(userID, "CONFIRM_DELETE_INCOME_MAIN")
					now := time.Now()
					monthNamesIndo := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
					currentMonthName := monthNamesIndo[int(now.Month())]
					replyText = fmt.Sprintf(`⚠️ **KONFIRMASI PENGHAPUSAN PEMASUKAN**<br><br>Apakah Anda yakin ingin menghapus data pemasukan Anda? Tindakan ini tidak dapat dibatalkan.<br><br>Pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Pemasukan Bulan Ini (%s %d)<br>**3** Hapus Pemasukan Berdasarkan Nama<br>**4** Hapus Pemasukan Bulan Tertentu<br>**5** Batal (Kembali)`, currentMonthName, now.Year())
				} else {
					var id, deskripsi string
					var nominal float64
					likeKeyword := "%" + cleanedMsg + "%"
					err := DB.QueryRow("SELECT id, deskripsi, nominal FROM transactions WHERE (deskripsi LIKE ? OR kategori LIKE ?) AND tipe = 'income' AND user_id = ? ORDER BY created_at DESC, id DESC LIMIT 1", likeKeyword, likeKeyword, userID).Scan(&id, &deskripsi, &nominal)
					if err == sql.ErrNoRows {
						replyText = fmt.Sprintf("Tidak ditemukan pemasukan yang cocok dengan kata kunci **\"%s\"**. 🤔<br>Silakan ketik nama/deskripsi pemasukan lain, atau ketik **kembali**.", cleanedMsg)
					} else if err != nil {
						log.Printf("DB Error finding income: %v", err)
						replyText = "Terjadi kesalahan saat mencari data pemasukan. 🥺"
					} else {
						SetUserState(userID, "CONFIRM_DELETE_LAST_"+id)
						replyText = fmt.Sprintf("Apakah Anda yakin ingin menghapus pemasukan **%s** sebesar **%s**? 🤔<br><br>**1** Ya, Hapus<br>**2** Batal (Kembali)", deskripsi, formatRupiah(nominal))
					}
				}
				isStateProcessed = true
			} else if currentState == "CONFIRM_DELETE_INCOME_MONTH" {
				if cleanedMsg == "kembali" || cleanedMsg == "batal" {
					SetUserState(userID, "CONFIRM_DELETE_INCOME_MAIN")
					now := time.Now()
					monthNamesIndo := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
					currentMonthName := monthNamesIndo[int(now.Month())]
					replyText = fmt.Sprintf(`⚠️ **KONFIRSEN HAPUS PEMASUKAN**<br><br>Apakah Anda yakin ingin menghapus data pemasukan Anda? Tindakan ini tidak dapat dibatalkan.<br><br>Pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Pemasukan Bulan Ini (%s %d)<br>**3** Hapus Pemasukan Berdasarkan Nama<br>**4** Hapus Pemasukan Bulan Tertentu<br>**5** Batal (Kembali)`, currentMonthName, now.Year())
				} else {
					month, year := parseMonthAndYear(cleanedMsg)
					hasMonthWord := false
					for name := range indoMonths {
						if strings.Contains(cleanedMsg, name) {
							hasMonthWord = true
							break
						}
					}
					if !hasMonthWord && !strings.Contains(cleanedMsg, "bulan lalu") {
						replyText = "Format bulan tidak dikenali. Silakan ketik nama bulan dan tahun dengan benar (Contoh: **Mei 2026**), atau ketik **kembali**."
					} else {
						salaryDay := GetSalaryDay(userID)
						start, end := getMonthCycleBounds(year, month, salaryDay)
						startStr := start.Format("2006-01-02")
						endStr := end.Format("2006-01-02")

						_, err := DB.Exec("DELETE FROM transactions WHERE tipe = 'income' AND tanggal >= ? AND tanggal <= ? AND user_id = ?", startStr, endStr, userID)
						if err != nil {
							log.Printf("DB Error deleting custom month incomes: %v", err)
							replyText = "Gagal menghapus data pemasukan untuk periode tersebut. 🥺"
						} else {
							SetUserState(userID, "")
							monthNamesIndo := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
							monthName := monthNamesIndo[month]
							periodLabel := fmt.Sprintf("%02d/%02d/%d s/d %02d/%02d/%d", start.Day(), start.Month(), start.Year(), end.Day(), end.Month(), end.Year())
							replyText = fmt.Sprintf("Data pemasukan untuk periode **%s %d** (%s) berhasil dihapus! 🗑️", monthName, year, periodLabel)
							_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", fmt.Sprintf("📢 **Notifikasi Sistem**: Data pemasukan untuk periode %s %d (%s) telah dihapus oleh pengguna.", monthName, year, periodLabel))
						}
					}
				}
				isStateProcessed = true
			} else if currentState == "CONFIRM_DELETE_ALL_MAIN" {
				if cleanedMsg == "4" || cleanedMsg == "kembali" || cleanedMsg == "batal" {
					SetUserState(userID, "")
					replyText = "Penghapusan data keuangan berhasil dibatalkan. Riwayat transaksi Anda tetap aman! 😌"
				} else if cleanedMsg == "1" {
					_, err := DB.Exec("DELETE FROM transactions WHERE user_id = ?", userID)
					if err != nil {
						log.Printf("DB Error deleting all transactions: %v", err)
						replyText = "Gagal menghapus data transaksi. 🥺"
					} else {
						SetUserState(userID, "")
						replyText = "⚠️ **PEMBERITAHUAN**: Seluruh data pemasukan dan pengeluaran Anda berhasil dihapus dari sistem! 🗑️"
						_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", "📢 **Notifikasi Sistem**: Seluruh data transaksi telah dihapus oleh pengguna.")
					}
				} else if cleanedMsg == "2" {
					_, err := DB.Exec("DELETE FROM transactions WHERE tipe = 'expense' AND user_id = ?", userID)
					if err != nil {
						log.Printf("DB Error deleting all expenses: %v", err)
						replyText = "Gagal menghapus data pengeluaran. 🥺"
					} else {
						SetUserState(userID, "")
						replyText = "Data seluruh pengeluaran Anda berhasil dihapus! 🗑️"
						_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", "📢 **Notifikasi Sistem**: Seluruh data pengeluaran telah dihapus oleh pengguna.")
					}
				} else if cleanedMsg == "3" {
					_, err := DB.Exec("DELETE FROM transactions WHERE tipe = 'income' AND user_id = ?", userID)
					if err != nil {
						log.Printf("DB Error deleting all incomes: %v", err)
						replyText = "Gagal menghapus data pemasukan. 🥺"
					} else {
						SetUserState(userID, "")
						replyText = "Data seluruh pemasukan Anda berhasil dihapus! 🗑️"
						_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", "📢 **Notifikasi Sistem**: Seluruh data pemasukan telah dihapus oleh pengguna.")
					}
				} else {
					replyText = "Pilihan tidak valid. Silakan pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Semua Pengeluaran Saja<br>**3** Hapus Semua Pemasukan Saja<br>**4** Batal (Kembali)"
				}
				isStateProcessed = true
			} else if strings.HasPrefix(currentState, "CONFIRM_DELETE_LAST_") {
				targetID := strings.TrimPrefix(currentState, "CONFIRM_DELETE_LAST_")
				if cleanedMsg == "1" || cleanedMsg == "ya" {
					var deskripsi string
					var nominal float64
					err := DB.QueryRow("SELECT deskripsi, nominal FROM transactions WHERE id = ? AND user_id = ?", targetID, userID).Scan(&deskripsi, &nominal)
					if err == nil {
						_, errDel := DB.Exec("DELETE FROM transactions WHERE id = ? AND user_id = ?", targetID, userID)
						if errDel == nil {
							replyText = fmt.Sprintf("Transaksi terakhir **%s** sebesar **%s** berhasil dihapus! 🗑️", deskripsi, formatRupiah(nominal))
						} else {
							replyText = "Gagal menghapus transaksi dari database. 🥺"
						}
					} else {
						replyText = "Transaksi sudah tidak ditemukan atau telah terhapus. 🤷‍♂️"
					}
					SetUserState(userID, "")
				} else {
					SetUserState(userID, "")
					replyText = "Penghapusan transaksi terakhir dibatalkan. Riwayat Anda tetap aman! 😌"
				}
				isStateProcessed = true
			} else if strings.HasPrefix(currentState, "CONFIRM_DELETE_TEMPLATE_") {
				targetID := strings.TrimPrefix(currentState, "CONFIRM_DELETE_TEMPLATE_")
				if cleanedMsg == "1" || cleanedMsg == "ya" {
					var deskripsi string
					err := DB.QueryRow("SELECT deskripsi FROM recurring_templates WHERE id = ? AND user_id = ?", targetID, userID).Scan(&deskripsi)
					if err == nil {
						_, errDel := DB.Exec("DELETE FROM recurring_templates WHERE id = ? AND user_id = ?", targetID, userID)
						if errDel == nil {
							replyText = fmt.Sprintf("Template transaksi bulanan **%s** berhasil dihapus! 🗑️", deskripsi)
						} else {
							replyText = "Gagal menghapus template dari database. 🥺"
						}
					} else {
						replyText = "Template sudah tidak ditemukan atau telah terhapus. 🤷‍♂️"
					}
					SetUserState(userID, "")
				} else {
					SetUserState(userID, "")
					replyText = "Penghapusan template dibatalkan. 😌"
				}
				isStateProcessed = true
			} else if strings.HasPrefix(currentState, "CONFIRM_UPDATE_TEMPLATE_") {
				parts := strings.Split(strings.TrimPrefix(currentState, "CONFIRM_UPDATE_TEMPLATE_"), "_")
				if len(parts) == 4 {
					templateID := parts[0]
					nominal, _ := strconv.ParseFloat(parts[1], 64)
					dayVal, _ := strconv.Atoi(parts[2])
					hasDay := parts[3] == "true"

					if cleanedMsg == "1" || cleanedMsg == "ya" {
						var deskripsi string
						err := DB.QueryRow("SELECT deskripsi FROM recurring_templates WHERE id = ? AND user_id = ?", templateID, userID).Scan(&deskripsi)
						if err == nil {
							var updateErr error
							if hasDay {
								_, updateErr = DB.Exec("UPDATE recurring_templates SET nominal = ?, target_day = ? WHERE id = ? AND user_id = ?", nominal, dayVal, templateID, userID)
							} else {
								_, updateErr = DB.Exec("UPDATE recurring_templates SET nominal = ? WHERE id = ? AND user_id = ?", nominal, templateID, userID)
							}

							if updateErr != nil {
								log.Printf("DB Error updating template: %v", updateErr)
								replyText = "Gagal memperbarui template di database. 🥺"
							} else {
								if hasDay {
									replyText = fmt.Sprintf("Template transaksi **%s** berhasil diperbarui menjadi **%s** setiap tanggal **%d**! ⚙️", deskripsi, formatRupiah(nominal), dayVal)
								} else {
									replyText = fmt.Sprintf("Template transaksi **%s** berhasil diperbarui menjadi **%s**! ⚙️", deskripsi, formatRupiah(nominal))
								}
								generateMissingTemplateTransactions(userID)
							}
						} else {
							replyText = "Template sudah tidak ditemukan atau telah terhapus. 🤷‍♂️"
						}
						SetUserState(userID, "")
					} else {
						SetUserState(userID, "")
						replyText = "Pembaruan template dibatalkan. 😌"
					}
				} else {
					SetUserState(userID, "")
					replyText = "State data tidak valid. Pembaruan dibatalkan. 🥺"
				}
				isStateProcessed = true
			} else if currentState == "CONFIRM_CREATE_TEMPLATES" {
				if cleanedMsg == "1" || cleanedMsg == "ya" {
					_, err := DB.Exec("UPDATE recurring_templates SET status = 'planned' WHERE user_id = ? AND status = 'pending'", userID)
					if err != nil {
						log.Printf("DB Error activating templates: %v", err)
						replyText = "Gagal mengaktifkan template di database. 🥺"
					} else {
						generateMissingTemplateTransactions(userID)

						// Fetch list of activated templates to show in response
						rows, err := DB.Query("SELECT deskripsi, nominal, target_day, tipe FROM recurring_templates WHERE user_id = ? AND status = 'planned'", userID)
						var lines []string
						if err == nil {
							defer rows.Close()
							for rows.Next() {
								var desc, tipe string
								var nominal float64
								var dayVal int
								if err := rows.Scan(&desc, &nominal, &dayVal, &tipe); err == nil {
									tipeLabel := "Pengeluaran"
									if tipe == "income" {
										tipeLabel = "Pemasukan"
									}
									lines = append(lines, fmt.Sprintf("• **%s** (%s): %s setiap tanggal **%d**", desc, tipeLabel, formatRupiah(nominal), dayVal))
								}
							}
						}
						replyText = fmt.Sprintf("Berhasil mengaktifkan template transaksi bulanan baru! ⚙️<br><br>%s<br><br>Transaksi ini akan otomatis dibuat setiap awal siklus gajian Anda! 🚀",
							strings.Join(lines, "<br>"))
					}
					SetUserState(userID, "")
				} else {
					_, _ = DB.Exec("DELETE FROM recurring_templates WHERE user_id = ? AND status = 'pending'", userID)
					SetUserState(userID, "")
					replyText = "Pendaftaran template dibatalkan. 😌"
				}
				isStateProcessed = true
			} else if strings.HasPrefix(currentState, "INTRO_") {
				if cleanedMsg == "kembali" {
					if currentState == "INTRO_MAIN" {
						SetUserState(userID, "")
						replyText = "Keluar dari perkenalan sistem. Silakan ketik transaksi Anda seperti biasa! 😊"
					} else {
						SetUserState(userID, "INTRO_MAIN")
						var content string
						err := DB.QueryRow("SELECT content FROM system_info WHERE topic_key = 'INTRO_MAIN'").Scan(&content)
						if err != nil {
							replyText = "Gagal memuat menu perkenalan. 🥺"
						} else {
							replyText = content
						}
					}
					isStateProcessed = true
				} else if cleanedMsg == "1" || cleanedMsg == "2" || cleanedMsg == "3" || cleanedMsg == "4" {
					nextState := "INTRO_" + cleanedMsg
					var content string
					err := DB.QueryRow("SELECT content FROM system_info WHERE topic_key = ?", nextState).Scan(&content)
					if err == sql.ErrNoRows {
						replyText = "Topik tidak ditemukan. 🥺"
					} else if err != nil {
						log.Printf("DB Error fetching info: %v", err)
						replyText = "Gagal memuat detail perkenalan. 🥺"
					} else {
						SetUserState(userID, nextState)
						replyText = content + "<br><br>Ketik **kembali** untuk kembali ke menu perkenalan."
					}
					isStateProcessed = true
				} else if cleanedMsg == "5" && currentState == "INTRO_MAIN" {
					SetUserState(userID, "HELP_MAIN")
					replyText = HelpMainMenuResponse
					isStateProcessed = true
				} else {
					// Check if user entered a real transaction or query
					if analysis.Intent != "UNKNOWN" {
						SetUserState(userID, "")
					} else {
						replyText = "Pilihan tidak valid. Silakan pilih nomor menu perkenalan yang tersedia (1-4), ketik **kembali**, atau ketik **halo** untuk ke Menu Perkenalan Utama."
						isStateProcessed = true
					}
				}
			} else {
				// 2. Check if user is in help state
				if cleanedMsg == "kembali" {
					if currentState == "HELP_MAIN" {
						SetUserState(userID, "")
						replyText = "Keluar dari bantuan. Silakan ketik transaksi Anda seperti biasa! 😊"
					} else {
						SetUserState(userID, "HELP_MAIN")
						replyText = HelpMainMenuResponse
					}
					isStateProcessed = true
				} else if currentState == "HELP_HAPUS_TRANSAKSI" && (cleanedMsg == "4.1" || cleanedMsg == "4.2") {
					now := time.Now()
					monthNamesIndo := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
					currentMonthName := monthNamesIndo[int(now.Month())]

					if cleanedMsg == "4.1" {
						SetUserState(userID, "CONFIRM_DELETE_EXPENSE_MAIN")
						replyText = fmt.Sprintf(`⚠️ **KONFIRMASI PENGHAPUSAN PENGELUARAN**<br><br>Apakah Anda yakin ingin menghapus data pengeluaran Anda? Tindakan ini tidak dapat dibatalkan.<br><br>Pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Pengeluaran Bulan Ini (%s %d)<br>**3** Hapus Pengeluaran Berdasarkan Nama<br>**4** Hapus Pengeluaran Bulan Tertentu<br>**5** Batal (Kembali)`, currentMonthName, now.Year())
					} else {
						SetUserState(userID, "CONFIRM_DELETE_INCOME_MAIN")
						replyText = fmt.Sprintf(`⚠️ **KONFIRMASI PENGHAPUSAN PEMASUKAN**<br><br>Apakah Anda yakin ingin menghapus data pemasukan Anda? Tindakan ini tidak dapat dibatalkan.<br><br>Pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Pemasukan Bulan Ini (%s %d)<br>**3** Hapus Pemasukan Berdasarkan Nama<br>**4** Hapus Pemasukan Bulan Tertentu<br>**5** Batal (Kembali)`, currentMonthName, now.Year())
					}
					isStateProcessed = true
				} else if opt, exists := HelpMenus[currentState][cleanedMsg]; exists {
					SetUserState(userID, opt.NextState)
					replyText = opt.Response
					isStateProcessed = true
				} else {
					// If not a help command, check if it's a valid non-UNKNOWN user transaction/query/CRUD
					if analysis.Intent != "UNKNOWN" {
						// User decided to execute a real command, break out of help
						SetUserState(userID, "")
					} else {
						// Invalid help input
						replyText = "Pilihan tidak valid. Silakan pilih nomor menu yang tersedia, ketik **kembali**, atau ketik **help** untuk ke Menu Utama."
						isStateProcessed = true
					}
				}
			}
		}
	}

	if !isStateProcessed {
		showDownloads := false
		lowerMsg := strings.ToLower(userMsg)
		downloadKeywords := []string{"laporan", "report", "rekap", "pdf", "xls", "xlsx", "excel", "word", "doc", "docx"}
		for _, kw := range downloadKeywords {
			if strings.Contains(lowerMsg, kw) {
				showDownloads = true
				break
			}
		}

		switch analysis.Intent {
		case "TRIGGER_RESET_DATA":
			tipe := "all"
			if t, ok := analysis.Data["tipe"].(string); ok {
				tipe = t
			}

			now := time.Now()
			monthNamesIndo := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
			currentMonthName := monthNamesIndo[int(now.Month())]

			if tipe == "income" {
				SetUserState(userID, "CONFIRM_DELETE_INCOME_MAIN")
				replyText = fmt.Sprintf(`⚠️ **KONFIRMASI PENGHAPUSAN PEMASUKAN**<br><br>Apakah Anda yakin ingin menghapus data pemasukan Anda? Tindakan ini tidak dapat dibatalkan.<br><br>Pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Pemasukan Bulan Ini (%s %d)<br>**3** Hapus Pemasukan Berdasarkan Nama<br>**4** Hapus Pemasukan Bulan Tertentu<br>**5** Batal (Kembali)`, currentMonthName, now.Year())
			} else if tipe == "expense" {
				SetUserState(userID, "CONFIRM_DELETE_EXPENSE_MAIN")
				replyText = fmt.Sprintf(`⚠️ **KONFIRMASI PENGHAPUSAN PENGELUARAN**<br><br>Apakah Anda yakin ingin menghapus data pengeluaran Anda? Tindakan ini tidak dapat dibatalkan.<br><br>Pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Pengeluaran Bulan Ini (%s %d)<br>**3** Hapus Pengeluaran Berdasarkan Nama<br>**4** Hapus Pengeluaran Bulan Tertentu<br>**5** Batal (Kembali)`, currentMonthName, now.Year())
			} else {
				SetUserState(userID, "CONFIRM_DELETE_ALL_MAIN")
				replyText = `⚠️ **KONFIRMASI PENGHAPUSAN SEMUA DATA**<br><br>Apakah Anda yakin ingin menghapus data keuangan Anda? Tindakan ini tidak dapat dibatalkan.<br><br>Pilih opsi penghapusan dengan mengetik nomor menu:<br><br>**1** Hapus Semua Data (Pemasukan & Pengeluaran)<br>**2** Hapus Semua Pengeluaran Saja<br>**3** Hapus Semua Pemasukan Saja<br>**4** Batal (Kembali)`
			}

		case "INPUT_TRANSAKSI":
			// Insert transaction to MySQL
			id := generateID()
			kategori := analysis.Data["kategori"].(string)
			tipe := analysis.Data["tipe"].(string)
			nominal := analysis.Data["nominal"].(float64)
			status := analysis.Data["status"].(string)
			deskripsi := analysis.Data["deskripsi"].(string)
			tanggal := analysis.Data["tanggal"].(string)

			var dueDate interface{} = nil
			if status == "planned" {
				// Set due date 10 days out
				dueDate = time.Now().AddDate(0, 0, 10).Format("2006-01-02")
			}

			_, err := DB.Exec("INSERT INTO transactions (id, user_id, tanggal, deskripsi, kategori, tipe, nominal, status, due_date, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				id, userID, tanggal, deskripsi, kategori, tipe, nominal, status, dueDate, time.Now())

			if err != nil {
				log.Printf("DB Error inserting transaction: %v", err)
				replyText = "Maaf, terjadi kesalahan saat menyimpan transaksi ke database. 🥺"
			} else {
				statusLabel := "Lunas (Paid)"
				if status == "planned" {
					statusLabel = "Direncanakan (Planned)"
				}
				typeLabel := "Pemasukan"
				if tipe == "expense" {
					typeLabel = "Pengeluaran"
				}
				replyText = fmt.Sprintf("Catatan berhasil disimpan! 📝<br><br>**%s**: %s<br>**Nominal**: %s<br>**Kategori**: %s<br>**Status**: %s",
					typeLabel, deskripsi, formatRupiah(nominal), kategori, statusLabel)
			}

		case "UPDATE_STATUS":
			category := analysis.Data["category"].(string)

			// Find oldest planned transaction for that category
			var id, deskripsi string
			var nominal float64
			err := DB.QueryRow("SELECT id, deskripsi, nominal FROM transactions WHERE kategori = ? AND status = 'planned' AND tipe = 'expense' AND user_id = ? ORDER BY tanggal ASC LIMIT 1", category, userID).Scan(&id, &deskripsi, &nominal)

			if err == sql.ErrNoRows {
				replyText = fmt.Sprintf("Tidak ada tagihan tertunda untuk kategori **\"%s\"** yang terdaftar. 😊<br><br>Mau catat transaksi baru? Contoh: \"listrik 200rb\"", category)
			} else if err != nil {
				log.Printf("DB Error finding planned tx: %v", err)
				replyText = "Maaf, ada kendala mencari tagihan di database. 🥺"
			} else {
				// Update status to paid
				_, err = DB.Exec("UPDATE transactions SET status = 'paid' WHERE id = ? AND user_id = ?", id, userID)
				if err != nil {
					log.Printf("DB Error updating status: %v", err)
					replyText = "Gagal memperbarui status tagihan. 🥺"
				} else {
					replyText = fmt.Sprintf("Tagihan **%s** sebesar **%s** berhasil dibayar! 💳<br>Status diperbarui menjadi Lunas (Paid).", deskripsi, formatRupiah(nominal))
				}
			}

		case "DELETE_TRANSAKSI":
			target := analysis.Data["target"].(string)
			keyword := analysis.Data["keyword"].(string)
			tipe := "all"
			if t, ok := analysis.Data["tipe"].(string); ok {
				tipe = t
			}

			if target == "last" {
				var id, deskripsi string
				var nominal float64
				err := DB.QueryRow("SELECT id, deskripsi, nominal FROM transactions WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT 1", userID).Scan(&id, &deskripsi, &nominal)

				if err == sql.ErrNoRows {
					replyText = "Tidak ada transaksi yang bisa dihapus. 🤷‍♂️"
				} else if err != nil {
					log.Printf("DB Error finding last transaction: %v", err)
					replyText = "Gagal mencari transaksi terakhir di database. 🥺"
				} else {
					SetUserState(userID, "CONFIRM_DELETE_LAST_"+id)
					replyText = fmt.Sprintf("Apakah Anda yakin ingin menghapus transaksi terakhir **%s** sebesar **%s**? 🤔<br><br>**1** Ya, Hapus<br>**2** Batal (Kembali)", deskripsi, formatRupiah(nominal))
				}
			} else {
				var id, deskripsi string
				var nominal float64

				likeKeyword := "%" + keyword + "%"
				var err error
				if tipe == "all" {
					err = DB.QueryRow("SELECT id, deskripsi, nominal FROM transactions WHERE (deskripsi LIKE ? OR kategori LIKE ?) AND user_id = ? ORDER BY created_at DESC, id DESC LIMIT 1", likeKeyword, likeKeyword, userID).Scan(&id, &deskripsi, &nominal)
				} else {
					err = DB.QueryRow("SELECT id, deskripsi, nominal FROM transactions WHERE (deskripsi LIKE ? OR kategori LIKE ?) AND tipe = ? AND user_id = ? ORDER BY created_at DESC, id DESC LIMIT 1", likeKeyword, likeKeyword, tipe, userID).Scan(&id, &deskripsi, &nominal)
				}

				if err == sql.ErrNoRows {
					if tipe == "expense" {
						replyText = fmt.Sprintf("Tidak ditemukan pengeluaran yang cocok dengan kata kunci **\"%s\"**. 🤔", keyword)
					} else if tipe == "income" {
						replyText = fmt.Sprintf("Tidak ditemukan pemasukan yang cocok dengan kata kunci **\"%s\"**. 🤔", keyword)
					} else {
						replyText = fmt.Sprintf("Tidak ditemukan transaksi yang cocok dengan kata kunci **\"%s\"**. 🤔", keyword)
					}
				} else if err != nil {
					log.Printf("DB Error finding transaction by keyword: %v", err)
					replyText = "Terjadi kesalahan saat mencari transaksi. 🥺"
				} else {
					SetUserState(userID, "CONFIRM_DELETE_LAST_"+id)
					replyText = fmt.Sprintf("Apakah Anda yakin ingin menghapus transaksi **%s** sebesar **%s**? 🤔<br><br>**1** Ya, Hapus<br>**2** Batal (Kembali)", deskripsi, formatRupiah(nominal))
				}
			}

		case "QUERY":
			qType := analysis.Data["queryType"].(string)
			replyText = handleQueryAnswerGo(qType, userID)

		case "DELETE_TEMPLATE":
			deskripsi, ok := analysis.Data["deskripsi"].(string)
			if !ok || deskripsi == "" {
				replyText = "Gagal memproses deskripsi template yang akan dihapus. 🥺"
				break
			}

			var templateID, realDesc string
			var nominal float64
			err := DB.QueryRow("SELECT id, deskripsi, nominal FROM recurring_templates WHERE user_id = ? AND LOWER(deskripsi) = LOWER(?)", userID, deskripsi).Scan(&templateID, &realDesc, &nominal)
			if err != nil {
				err = DB.QueryRow("SELECT id, deskripsi, nominal FROM recurring_templates WHERE user_id = ? AND LOWER(deskripsi) LIKE LOWER(?)", userID, "%"+deskripsi+"%").Scan(&templateID, &realDesc, &nominal)
			}

			if err != nil {
				replyText = fmt.Sprintf("Template transaksi dengan deskripsi **%s** tidak ditemukan. 🥺", deskripsi)
			} else {
				SetUserState(userID, "CONFIRM_DELETE_TEMPLATE_"+templateID)
				replyText = fmt.Sprintf("Apakah Anda yakin ingin menghapus template transaksi bulanan **%s** sebesar **%s**? 🤔<br><br>**1** Ya, Hapus<br>**2** Batal (Kembali)", realDesc, formatRupiah(nominal))
			}

		case "UPDATE_TEMPLATE":
			targetDesc, okDesc := analysis.Data["deskripsi_target"].(string)
			nominal, okNom := analysis.Data["nominal"].(float64)
			dayVal, okDay := analysis.Data["day"].(int)
			hasDay := analysis.Data["has_day"].(bool)

			if !okDesc || !okNom || !okDay {
				replyText = "Gagal memproses data pembaruan template. 🥺"
				break
			}

			var templateID, realDesc string
			err := DB.QueryRow("SELECT id, deskripsi FROM recurring_templates WHERE user_id = ? AND LOWER(deskripsi) = LOWER(?)", userID, targetDesc).Scan(&templateID, &realDesc)
			if err != nil {
				err = DB.QueryRow("SELECT id, deskripsi FROM recurring_templates WHERE user_id = ? AND LOWER(deskripsi) LIKE LOWER(?)", userID, "%"+targetDesc+"%").Scan(&templateID, &realDesc)
			}

			if err != nil {
				replyText = fmt.Sprintf("Template transaksi dengan deskripsi **%s** tidak ditemukan. 🥺", targetDesc)
			} else {
				hasDayStr := "false"
				if hasDay {
					hasDayStr = "true"
				}
				stateStr := fmt.Sprintf("CONFIRM_UPDATE_TEMPLATE_%s_%.2f_%d_%s", templateID, nominal, dayVal, hasDayStr)
				SetUserState(userID, stateStr)

				if hasDay {
					replyText = fmt.Sprintf("Apakah Anda yakin ingin mengubah template **%s** menjadi **%s** setiap tanggal **%d**? 🤔<br><br>**1** Ya, Ubah<br>**2** Batal (Kembali)",
						realDesc, formatRupiah(nominal), dayVal)
				} else {
					replyText = fmt.Sprintf("Apakah Anda yakin ingin mengubah nominal template **%s** menjadi **%s**? 🤔<br><br>**1** Ya, Ubah<br>**2** Batal (Kembali)",
						realDesc, formatRupiah(nominal))
				}
			}

		case "CREATE_TEMPLATES":
			templatesData, ok := analysis.Data["templates"].([]map[string]interface{})
			if !ok {
				replyText = "Gagal memproses daftar template transaksi. 🥺"
				break
			}

			_, _ = DB.Exec("DELETE FROM recurring_templates WHERE user_id = ? AND status = 'pending'", userID)

			var lines []string
			successCount := 0

			for _, tmpl := range templatesData {
				tipe := tmpl["tipe"].(string)
				kategori := tmpl["kategori"].(string)
				nominal := tmpl["nominal"].(float64)
				deskripsi := tmpl["deskripsi"].(string)
				dayVal := tmpl["day"].(int)

				id := generateID()

				_, err := DB.Exec("INSERT INTO recurring_templates (id, user_id, tipe, kategori, nominal, deskripsi, target_day, status) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending')",
					id, userID, tipe, kategori, nominal, deskripsi, dayVal)
				if err != nil {
					log.Printf("DB Error inserting template: %v", err)
					continue
				}

				successCount++

				tipeLabel := "Pengeluaran"
				if tipe == "income" {
					tipeLabel = "Pemasukan"
				}

				lines = append(lines, fmt.Sprintf("• **%s** (%s): %s setiap tanggal **%d**", deskripsi, tipeLabel, formatRupiah(nominal), dayVal))
			}

			if successCount == 0 {
				replyText = "Gagal menyimpan template transaksi ke database. 🥺"
			} else {
				SetUserState(userID, "CONFIRM_CREATE_TEMPLATES")
				replyText = fmt.Sprintf("Apakah Anda yakin ingin menambahkan **%d** template transaksi bulanan berikut? 🤔<br><br>%s<br><br>**1** Ya, Simpan<br>**2** Batal (Kembali)",
					successCount, strings.Join(lines, "<br>"))
			}

		case "CREATE_CATEGORY":
			id := analysis.Data["id"].(string)
			label := analysis.Data["label"].(string)
			tipe := analysis.Data["tipe"].(string)

			_, err := DB.Exec("INSERT INTO categories (id, label, tipe, keywords) VALUES (?, ?, ?, '')", id, label, tipe)
			if err != nil {
				log.Printf("DB Error creating category: %v", err)
				if strings.Contains(err.Error(), "Duplicate entry") {
					replyText = fmt.Sprintf("Kategori dengan ID **%s** sudah terdaftar! 📁", id)
				} else {
					replyText = "Gagal membuat kategori baru di database. 🥺"
				}
			} else {
				ClearCategoryCache()
				replyText = fmt.Sprintf("Kategori baru **%s** (%s) dengan ID **%s** berhasil ditambahkan! 📁", label, tipe, id)
			}

		case "LIST_CATEGORIES":
			rows, err := DB.Query("SELECT id, label, tipe FROM categories ORDER BY tipe ASC, label ASC")
			if err != nil {
				log.Printf("DB Error listing categories: %v", err)
				replyText = "Gagal memuat daftar kategori. 🥺"
			} else {
				defer rows.Close()
				var lines []string
				for rows.Next() {
					var id, label, tipe string
					if errScan := rows.Scan(&id, &label, &tipe); errScan == nil {
						tipeLabel := "Pengeluaran"
						if tipe == "income" {
							tipeLabel = "Pemasukan"
						}
						lines = append(lines, fmt.Sprintf("• **%s** (%s) - ID: `%s`", label, tipeLabel, id))
					}
				}
				if len(lines) == 0 {
					replyText = "Belum ada kategori terdaftar. 🤷‍♂️"
				} else {
					replyText = "📁 **Daftar Kategori Terdaftar**:<br><br>" + strings.Join(lines, "<br>")
				}
			}

		case "DELETE_CATEGORY":
			id := analysis.Data["id"].(string)

			// Check if category exists
			var label string
			err := DB.QueryRow("SELECT label FROM categories WHERE id = ?", id).Scan(&label)
			if err == sql.ErrNoRows {
				replyText = fmt.Sprintf("Kategori dengan ID **%s** tidak ditemukan. 🤷‍♂️", id)
			} else if err != nil {
				log.Printf("DB Error checking category: %v", err)
				replyText = "Gagal memvalidasi kategori. 🥺"
			} else {
				_, err = DB.Exec("DELETE FROM categories WHERE id = ?", id)
				if err != nil {
					log.Printf("DB Error deleting category: %v", err)
					replyText = fmt.Sprintf("Gagal menghapus kategori **%s**. 🥺", label)
				} else {
					ClearCategoryCache()
					replyText = fmt.Sprintf("Kategori **%s** (ID: `%s`) berhasil dihapus! 🗑️", label, id)
				}
			}

		case "ADD_KEYWORDS":
			catID := analysis.Data["category_id"].(string)
			newKwsInput := analysis.Data["keywords"].(string)

			// Check if category exists
			var label, currentKwsStr string
			err := DB.QueryRow("SELECT label, keywords FROM categories WHERE id = ?", catID).Scan(&label, &currentKwsStr)
			if err == sql.ErrNoRows {
				replyText = fmt.Sprintf("Kategori dengan ID **%s** tidak ditemukan. 🤷‍♂️", catID)
			} else if err != nil {
				log.Printf("DB Error checking category: %v", err)
				replyText = "Gagal memvalidasi kategori. 🥺"
			} else {
				// Parse new keywords
				var addedKws []string
				kwMap := make(map[string]bool)
				for _, kw := range strings.Split(currentKwsStr, ",") {
					kwClean := strings.TrimSpace(strings.ToLower(kw))
					if kwClean != "" {
						kwMap[kwClean] = true
					}
				}

				for _, kw := range strings.Split(newKwsInput, ",") {
					kwClean := strings.TrimSpace(strings.ToLower(kw))
					if kwClean != "" && !kwMap[kwClean] {
						kwMap[kwClean] = true
						addedKws = append(addedKws, kwClean)
					}
				}

				if len(addedKws) == 0 {
					replyText = fmt.Sprintf("Semua keyword tersebut sudah terdaftar di kategori **%s**. 📝", label)
				} else {
					// Reconstruct all keywords
					var allKws []string
					for kw := range kwMap {
						allKws = append(allKws, kw)
					}
					updatedKwsStr := strings.Join(allKws, ",")

					_, err = DB.Exec("UPDATE categories SET keywords = ? WHERE id = ?", updatedKwsStr, catID)
					if err != nil {
						log.Printf("DB Error updating keywords: %v", err)
						replyText = "Gagal menyimpan keyword baru ke database. 🥺"
					} else {
						ClearCategoryCache()
						replyText = fmt.Sprintf("Berhasil menambahkan keyword baru ke kategori **%s** (ID: `%s`):<br>**%s** 📝", label, catID, strings.Join(addedKws, ", "))
					}
				}
			}

		case "DELETE_KEYWORDS":
			catID := analysis.Data["category_id"].(string)
			kwsToDeleteInput := analysis.Data["keywords"].(string)

			// Check if category exists
			var label, currentKwsStr string
			err := DB.QueryRow("SELECT label, keywords FROM categories WHERE id = ?", catID).Scan(&label, &currentKwsStr)
			if err == sql.ErrNoRows {
				replyText = fmt.Sprintf("Kategori dengan ID **%s** tidak ditemukan. 🤷‍♂️", catID)
			} else if err != nil {
				log.Printf("DB Error checking category: %v", err)
				replyText = "Gagal memvalidasi kategori. 🥺"
			} else {
				// Parse keywords to delete
				delMap := make(map[string]bool)
				for _, kw := range strings.Split(kwsToDeleteInput, ",") {
					kwClean := strings.TrimSpace(strings.ToLower(kw))
					if kwClean != "" {
						delMap[kwClean] = true
					}
				}

				// Filter existing keywords
				var remainingKws []string
				var removedKws []string
				for _, kw := range strings.Split(currentKwsStr, ",") {
					kwClean := strings.TrimSpace(strings.ToLower(kw))
					if kwClean == "" {
						continue
					}
					if delMap[kwClean] {
						removedKws = append(removedKws, kwClean)
					} else {
						remainingKws = append(remainingKws, kwClean)
					}
				}

				if len(removedKws) == 0 {
					replyText = fmt.Sprintf("Tidak ada keyword yang cocok untuk dihapus dari kategori **%s**. 🤔", label)
				} else {
					updatedKwsStr := strings.Join(remainingKws, ",")
					_, err = DB.Exec("UPDATE categories SET keywords = ? WHERE id = ?", updatedKwsStr, catID)
					if err != nil {
						log.Printf("DB Error updating keywords: %v", err)
						replyText = "Gagal memperbarui keyword di database. 🥺"
					} else {
						ClearCategoryCache()
						replyText = fmt.Sprintf("Berhasil menghapus keyword dari kategori **%s** (ID: `%s`):<br>**%s** 🗑️", label, catID, strings.Join(removedKws, ", "))
					}
				}
			}

		case "LIST_KEYWORDS":
			catID := analysis.Data["category_id"].(string)

			// Check if category exists
			var label, keywordsStr string
			err := DB.QueryRow("SELECT label, keywords FROM categories WHERE id = ?", catID).Scan(&label, &keywordsStr)
			if err == sql.ErrNoRows {
				replyText = fmt.Sprintf("Kategori dengan ID **%s** tidak ditemukan. 🤷‍♂️", catID)
			} else if err != nil {
				log.Printf("DB Error checking category: %v", err)
				replyText = "Gagal memvalidasi kategori. 🥺"
			} else {
				var kws []string
				for _, kw := range strings.Split(keywordsStr, ",") {
					kwClean := strings.TrimSpace(kw)
					if kwClean != "" {
						kws = append(kws, kwClean)
					}
				}

				if len(kws) == 0 {
					replyText = fmt.Sprintf("Kategori **%s** (ID: `%s`) belum memiliki keyword. 📝", label, catID)
				} else {
					replyText = fmt.Sprintf("📝 **Keyword Kategori %s** (ID: `%s`):<br><br>%s", label, catID, strings.Join(kws, ", "))
				}
			}

		case "QUERY_SALARY_DAY":
			dayVal := GetSalaryDay(userID)
			now := time.Now()
			start, end := getMonthCycleBounds(now.Year(), int(now.Month()), dayVal)
			if dayVal == 1 {
				replyText = fmt.Sprintf("Tanggal gajian Anda diatur ke tanggal **%d** (Menggunakan siklus bulan kalender standar). ⚙️<br><br>Siklus finansial Anda bulan ini: **%s** s/d **%s**.",
					dayVal, formatDateIndo(start), formatDateIndo(end))
			} else {
				replyText = fmt.Sprintf("Tanggal gajian Anda diatur ke tanggal **%d** setiap bulannya. ⚙️<br><br>Siklus finansial Anda bulan ini: **%s** s/d **%s**.",
					dayVal, formatDateIndo(start), formatDateIndo(end))
			}

		case "SET_SALARY_DAY":
			dayVal := 1
			if dVal, ok := analysis.Data["day"].(int); ok {
				dayVal = dVal
			} else if dValFloat, ok := analysis.Data["day"].(float64); ok {
				dayVal = int(dValFloat)
			}
			SetSalaryDay(userID, dayVal)

			if dayVal == 1 {
				replyText = "Tanggal gajian berhasil diatur ke tanggal **1** (Menggunakan siklus bulan kalender standar). ⚙️"
			} else {
				now := time.Now()
				start, end := getMonthCycleBounds(now.Year(), int(now.Month()), dayVal)
				replyText = fmt.Sprintf("Tanggal gajian berhasil diatur ke tanggal **%d** setiap bulannya. ⚙️<br><br>Siklus finansial Anda bulan ini: **%s** s/d **%s**.",
					dayVal, formatDateIndo(start), formatDateIndo(end))
			}

		case "DOWNLOAD_TRANSACTIONS":
			tipe := "expense"
			if tVal, ok := analysis.Data["tipe"].(string); ok {
				tipe = tVal
			}

			month := int(time.Now().Month())
			if mVal, ok := analysis.Data["month"].(int); ok {
				month = mVal
			} else if mValFloat, ok := analysis.Data["month"].(float64); ok {
				month = int(mValFloat)
			}

			year := time.Now().Year()
			if yVal, ok := analysis.Data["year"].(int); ok {
				year = yVal
			} else if yValFloat, ok := analysis.Data["year"].(float64); ok {
				year = int(yValFloat)
			}

			format := "pdf"
			if fVal, ok := analysis.Data["format"].(string); ok {
				format = fVal
			}

			monthNamesIndo := []string{
				"", "Januari", "Februari", "Maret", "April", "Mei", "Juni",
				"Juli", "Agustus", "September", "Oktober", "November", "Desember",
			}
			monthName := "Bulan"
			if month >= 1 && month <= 12 {
				monthName = monthNamesIndo[month]
			}

			typeLabel := "Pengeluaran"
			if tipe == "income" {
				typeLabel = "Pemasukan"
			} else if tipe == "all" {
				typeLabel = "Transaksi"
			}

			formatLabel := strings.ToUpper(format)
			fileExt := "." + format
			if format == "xls" {
				fileExt = ".xlsx"
				formatLabel = "Excel (XLSX)"
			} else if format == "word" {
				fileExt = ".doc"
				formatLabel = "Word (DOC)"
			}

			salaryDay := GetSalaryDay(userID)
			start, end := getMonthCycleBounds(year, month, salaryDay)
			startStr := start.Format("2006-01-02")
			endStr := end.Format("2006-01-02")

			downloadURL := fmt.Sprintf("/api/download/%s?tipe=%s&start_date=%s&end_date=%s", format, tipe, startStr, endStr)
			fileName := fmt.Sprintf("laporan_%s_%s_%s%s", tipe, strings.ReplaceAll(startStr, "-", ""), strings.ReplaceAll(endStr, "-", ""), fileExt)

			iconColor := "#f43f5e"
			iconText := "PDF"
			if format == "xls" {
				iconColor = "#10b981"
				iconText = "XLSX"
			} else if format == "word" {
				iconColor = "#3b82f6"
				iconText = "DOC"
			}

			replyText = fmt.Sprintf(`Silakan unduh rincian **%s** Anda untuk **%s %d** (%s s/d %s):<br><a href="%s" download class="block mt-2 no-underline"><div class="flex items-center gap-3 p-2.5 bg-[#111b21] hover:bg-[#202c33] rounded border border-white/10 transition cursor-pointer text-[#e9edef]"><div class="w-9 h-9 rounded flex justify-center items-center font-bold text-white text-[10px] shrink-0" style="background-color: %s;">%s</div><div class="flex-grow min-w-0 leading-tight"><div class="font-semibold text-[11px] truncate">%s</div><div class="text-[9px] text-[#8696a0] mt-0.5">Unduh dokumen %s</div></div><div class="text-[#00a884] shrink-0"><svg class="w-5 h-5" fill="none" stroke="currentColor" stroke-width="2.5" viewBox="0 0 24 24" style="width:20px;height:20px;"><path stroke-linecap="round" stroke-linejoin="round" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"></path></svg></div></div></a>`,
				typeLabel, monthName, year, formatDateIndo(start), formatDateIndo(end), downloadURL, iconColor, iconText, fileName, formatLabel)

		case "DOWNLOAD_TRANSACTIONS_RANGE":
			tipe := "expense"
			if tVal, ok := analysis.Data["tipe"].(string); ok {
				tipe = tVal
			}

			startStr := analysis.Data["start_date"].(string)
			endStr := analysis.Data["end_date"].(string)

			format := "pdf"
			if fVal, ok := analysis.Data["format"].(string); ok {
				format = fVal
			}

			typeLabel := "Pengeluaran"
			if tipe == "income" {
				typeLabel = "Pemasukan"
			} else if tipe == "all" {
				typeLabel = "Transaksi"
			}

			formatLabel := strings.ToUpper(format)
			fileExt := "." + format
			if format == "xls" {
				fileExt = ".xlsx"
				formatLabel = "Excel (XLSX)"
			} else if format == "word" {
				fileExt = ".doc"
				formatLabel = "Word (DOC)"
			}

			downloadURL := fmt.Sprintf("/api/download/%s?tipe=%s&start_date=%s&end_date=%s", format, tipe, startStr, endStr)
			fileName := fmt.Sprintf("laporan_%s_%s_%s%s", tipe, strings.ReplaceAll(startStr, "-", ""), strings.ReplaceAll(endStr, "-", ""), fileExt)

			iconColor := "#f43f5e"
			iconText := "PDF"
			if format == "xls" {
				iconColor = "#10b981"
				iconText = "XLSX"
			} else if format == "word" {
				iconColor = "#3b82f6"
				iconText = "DOC"
			}

			periodLabel := fmt.Sprintf("%s s/d %s", formatStrDateIndo(startStr), formatStrDateIndo(endStr))

			replyText = fmt.Sprintf(`Silakan unduh rincian **%s** Anda untuk periode **%s**:<br><a href="%s" download class="block mt-2 no-underline"><div class="flex items-center gap-3 p-2.5 bg-[#111b21] hover:bg-[#202c33] rounded border border-white/10 transition cursor-pointer text-[#e9edef]"><div class="w-9 h-9 rounded flex justify-center items-center font-bold text-white text-[10px] shrink-0" style="background-color: %s;">%s</div><div class="flex-grow min-w-0 leading-tight"><div class="font-semibold text-[11px] truncate">%s</div><div class="text-[9px] text-[#8696a0] mt-0.5">Unduh dokumen %s</div></div><div class="text-[#00a884] shrink-0"><svg class="w-5 h-5" fill="none" stroke="currentColor" stroke-width="2.5" viewBox="0 0 24 24" style="width:20px;height:20px;"><path stroke-linecap="round" stroke-linejoin="round" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"></path></svg></div></div></a>`,
				typeLabel, periodLabel, downloadURL, iconColor, iconText, fileName, formatLabel)

		case "LIST_TRANSACTIONS_BY_MONTH":
			tipe := "expense"
			if tVal, ok := analysis.Data["tipe"].(string); ok {
				tipe = tVal
			}

			month := int(time.Now().Month())
			if mVal, ok := analysis.Data["month"].(int); ok {
				month = mVal
			} else if mValFloat, ok := analysis.Data["month"].(float64); ok {
				month = int(mValFloat)
			}

			year := time.Now().Year()
			if yVal, ok := analysis.Data["year"].(int); ok {
				year = yVal
			} else if yValFloat, ok := analysis.Data["year"].(float64); ok {
				year = int(yValFloat)
			}

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

			monthNamesIndo := []string{
				"", "Januari", "Februari", "Maret", "April", "Mei", "Juni",
				"Juli", "Agustus", "September", "Oktober", "November", "Desember",
			}
			monthName := "Bulan"
			if month >= 1 && month <= 12 {
				monthName = monthNamesIndo[month]
			}

			salaryDay := GetSalaryDay(userID)
			start, end := getMonthCycleBounds(year, month, salaryDay)
			startStr := start.Format("2006-01-02")
			endStr := end.Format("2006-01-02")

			var rows *sql.Rows
			var err error
			if tipe == "all" {
				rows, err = DB.Query("SELECT tanggal, deskripsi, kategori, tipe, nominal, status FROM transactions WHERE tanggal >= ? AND tanggal <= ? AND user_id = ? ORDER BY tanggal ASC, created_at ASC", startStr, endStr, userID)
			} else {
				rows, err = DB.Query("SELECT tanggal, deskripsi, kategori, tipe, nominal, status FROM transactions WHERE tipe = ? AND tanggal >= ? AND tanggal <= ? AND user_id = ? ORDER BY tanggal ASC, created_at ASC", tipe, startStr, endStr, userID)
			}

			if err != nil {
				log.Printf("DB Error querying transactions by month: %v", err)
				replyText = "Gagal memuat rincian transaksi dari database. 🥺"
			} else {
				defer rows.Close()
				periodLabel := fmt.Sprintf("%s %d (%s s/d %s)", monthName, year, formatDateIndo(start), formatDateIndo(end))
				downloadPDF := fmt.Sprintf("/api/download/pdf?tipe=%s&start_date=%s&end_date=%s&user_id=%s", tipe, startStr, endStr, userID)
				downloadXLS := fmt.Sprintf("/api/download/xls?tipe=%s&start_date=%s&end_date=%s&user_id=%s", tipe, startStr, endStr, userID)
				downloadWord := fmt.Sprintf("/api/download/word?tipe=%s&start_date=%s&end_date=%s&user_id=%s", tipe, startStr, endStr, userID)

				if tipe == "all" {
					var incomes []string
					var expenses []string
					var totalIncome, totalExpense float64
					for rows.Next() {
						var tanggal, deskripsi, kategori, rowTipe, status string
						var nominal float64
						if errScan := rows.Scan(&tanggal, &deskripsi, &kategori, &rowTipe, &nominal, &status); errScan == nil {
							dateStr := tanggal
							if len(tanggal) >= 10 {
								dateStr = tanggal[8:10] + "/" + tanggal[5:7]
							}

							catLabel, exists := catLabels[kategori]
							if !exists {
								catLabel = kategori
								if len(catLabel) > 0 {
									catLabel = strings.ToUpper(catLabel[0:1]) + catLabel[1:]
								}
							}

							statusSuffix := ""
							if status == "planned" {
								statusSuffix = " *(Planned)*"
							}

							line := fmt.Sprintf("• [%s] %s (%s): **%s**%s", dateStr, deskripsi, catLabel, formatRupiah(nominal), statusSuffix)
							if rowTipe == "income" {
								incomes = append(incomes, line)
								totalIncome += nominal
							} else {
								expenses = append(expenses, line)
								totalExpense += nominal
							}
						}
					}

					if len(incomes) == 0 && len(expenses) == 0 {
						replyText = fmt.Sprintf("Tidak ada transaksi yang tercatat untuk periode **%s**. 🤷‍♂️", periodLabel)
					} else {
						incomeListStr := "Belum ada pemasukan"
						if len(incomes) > 0 {
							incomeListStr = strings.Join(incomes, "<br>")
						}

						expenseListStr := "Belum ada pengeluaran"
						if len(expenses) > 0 {
							expenseListStr = strings.Join(expenses, "<br>")
						}

						saldo := totalIncome - totalExpense

						if showDownloads {
							replyText = fmt.Sprintf(`📊 **LAPORAN KEUANGAN BULAN INI**<br>Periode: **%s**<br><br>📥 **PEMASUKAN (INCOME)**<br>%s<br>Total Pemasukan: **%s**<br><br>📤 **PENGELUARAN (EXPENSE)**<br>%s<br>Total Pengeluaran: **%s**<br><br>💰 **SALDO & RINGKASAN**<br>Saldo saat ini: **%s**<br><br>📄 **UNDUH LAPORAN**<br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh PDF</a><br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh Excel</a><br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh Word</a>`,
								periodLabel, incomeListStr, formatRupiah(totalIncome), expenseListStr, formatRupiah(totalExpense), formatRupiah(saldo), downloadPDF, downloadXLS, downloadWord)
						} else {
							replyText = fmt.Sprintf(`📊 **LAPORAN KEUANGAN BULAN INI**<br>Periode: **%s**<br><br>📥 **PEMASUKAN (INCOME)**<br>%s<br>Total Pemasukan: **%s**<br><br>📤 **PENGELUARAN (EXPENSE)**<br>%s<br>Total Pengeluaran: **%s**<br><br>💰 **SALDO & RINGKASAN**<br>Saldo saat ini: **%s**`,
								periodLabel, incomeListStr, formatRupiah(totalIncome), expenseListStr, formatRupiah(totalExpense), formatRupiah(saldo))
						}
					}
				} else {
					var lines []string
					var totalAmount float64
					for rows.Next() {
						var tanggal, deskripsi, kategori, rowTipe, status string
						var nominal float64
						if errScan := rows.Scan(&tanggal, &deskripsi, &kategori, &rowTipe, &nominal, &status); errScan == nil {
							dateStr := tanggal
							if len(tanggal) >= 10 {
								dateStr = tanggal[8:10] + "/" + tanggal[5:7]
							}

							catLabel, exists := catLabels[kategori]
							if !exists {
								catLabel = kategori
								if len(catLabel) > 0 {
									catLabel = strings.ToUpper(catLabel[0:1]) + catLabel[1:]
								}
							}

							statusSuffix := ""
							if status == "planned" {
								statusSuffix = " *(Planned)*"
							}

							line := fmt.Sprintf("• [%s] %s (%s): **%s**%s", dateStr, deskripsi, catLabel, formatRupiah(nominal), statusSuffix)
							lines = append(lines, line)
							totalAmount += nominal
						}
					}

					typeLabel := "pengeluaran"
					totalLabel := "Pengeluaran"
					header := "🧾 **Daftar Rincian Pengeluaran**"
					if tipe == "income" {
						typeLabel = "pemasukan"
						totalLabel = "Pemasukan"
						header = "🧾 **Daftar Rincian Pemasukan**"
					}

					if len(lines) == 0 {
						replyText = fmt.Sprintf("Tidak ada %s yang tercatat untuk periode **%s**. 🤷‍♂️", typeLabel, periodLabel)
					} else {
						if showDownloads {
							replyText = fmt.Sprintf(`%s (%s):<br><br>%s<br><br>**Total %s**: %s<br><br>📄 **UNDUH LAPORAN**<br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh PDF</a><br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh Excel</a><br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh Word</a>`,
								header, periodLabel, strings.Join(lines, "<br>"), totalLabel, formatRupiah(totalAmount), downloadPDF, downloadXLS, downloadWord)
						} else {
							replyText = fmt.Sprintf(`%s (%s):<br><br>%s<br><br>**Total %s**: %s`,
								header, periodLabel, strings.Join(lines, "<br>"), totalLabel, formatRupiah(totalAmount))
						}
					}
				}
			}

		case "LIST_TRANSACTIONS_RANGE":
			tipe := "expense"
			if tVal, ok := analysis.Data["tipe"].(string); ok {
				tipe = tVal
			}

			startStr := analysis.Data["start_date"].(string)
			endStr := analysis.Data["end_date"].(string)

			tStart, _ := time.Parse("2006-01-02", startStr)
			tEnd, _ := time.Parse("2006-01-02", endStr)

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

			var rows *sql.Rows
			var err error
			if tipe == "all" {
				rows, err = DB.Query("SELECT tanggal, deskripsi, kategori, tipe, nominal, status FROM transactions WHERE tanggal >= ? AND tanggal <= ? AND user_id = ? ORDER BY tanggal ASC, created_at ASC", startStr, endStr, userID)
			} else {
				rows, err = DB.Query("SELECT tanggal, deskripsi, kategori, tipe, nominal, status FROM transactions WHERE tipe = ? AND tanggal >= ? AND tanggal <= ? AND user_id = ? ORDER BY tanggal ASC, created_at ASC", tipe, startStr, endStr, userID)
			}

			if err != nil {
				log.Printf("DB Error querying transactions by range: %v", err)
				replyText = "Gagal memuat rincian transaksi dari database. 🥺"
			} else {
				defer rows.Close()
				periodLabel := fmt.Sprintf("%s s/d %s", formatDateIndo(tStart), formatDateIndo(tEnd))
				downloadPDF := fmt.Sprintf("/api/download/pdf?tipe=%s&start_date=%s&end_date=%s&user_id=%s", tipe, startStr, endStr, userID)
				downloadXLS := fmt.Sprintf("/api/download/xls?tipe=%s&start_date=%s&end_date=%s&user_id=%s", tipe, startStr, endStr, userID)
				downloadWord := fmt.Sprintf("/api/download/word?tipe=%s&start_date=%s&end_date=%s&user_id=%s", tipe, startStr, endStr, userID)

				if tipe == "all" {
					var incomes []string
					var expenses []string
					var totalIncome, totalExpense float64
					for rows.Next() {
						var tanggal, deskripsi, kategori, rowTipe, status string
						var nominal float64
						if errScan := rows.Scan(&tanggal, &deskripsi, &kategori, &rowTipe, &nominal, &status); errScan == nil {
							dateStr := tanggal
							if len(tanggal) >= 10 {
								dateStr = tanggal[8:10] + "/" + tanggal[5:7]
							}

							catLabel, exists := catLabels[kategori]
							if !exists {
								catLabel = kategori
								if len(catLabel) > 0 {
									catLabel = strings.ToUpper(catLabel[0:1]) + catLabel[1:]
								}
							}

							statusSuffix := ""
							if status == "planned" {
								statusSuffix = " *(Planned)*"
							}

							line := fmt.Sprintf("• [%s] %s (%s): **%s**%s", dateStr, deskripsi, catLabel, formatRupiah(nominal), statusSuffix)
							if rowTipe == "income" {
								incomes = append(incomes, line)
								totalIncome += nominal
							} else {
								expenses = append(expenses, line)
								totalExpense += nominal
							}
						}
					}

					if len(incomes) == 0 && len(expenses) == 0 {
						replyText = fmt.Sprintf("Tidak ada transaksi yang tercatat untuk periode **%s**. 🤷‍♂️", periodLabel)
					} else {
						incomeListStr := "Belum ada pemasukan"
						if len(incomes) > 0 {
							incomeListStr = strings.Join(incomes, "<br>")
						}

						expenseListStr := "Belum ada pengeluaran"
						if len(expenses) > 0 {
							expenseListStr = strings.Join(expenses, "<br>")
						}

						saldo := totalIncome - totalExpense

						if showDownloads {
							replyText = fmt.Sprintf(`📊 **LAPORAN KEUANGAN KUSTOM**<br>Periode: **%s**<br><br>📥 **PEMASUKAN (INCOME)**<br>%s<br>Total Pemasukan: **%s**<br><br>📤 **PENGELUARAN (EXPENSE)**<br>%s<br>Total Pengeluaran: **%s**<br><br>💰 **SALDO & RINGKASAN**<br>Saldo saat ini: **%s**<br><br>📄 **UNDUH LAPORAN**<br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh PDF</a><br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh Excel</a><br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh Word</a>`,
								periodLabel, incomeListStr, formatRupiah(totalIncome), expenseListStr, formatRupiah(totalExpense), formatRupiah(saldo), downloadPDF, downloadXLS, downloadWord)
						} else {
							replyText = fmt.Sprintf(`📊 **LAPORAN KEUANGAN KUSTOM**<br>Periode: **%s**<br><br>📥 **PEMASUKAN (INCOME)**<br>%s<br>Total Pemasukan: **%s**<br><br>📤 **PENGELUARAN (EXPENSE)**<br>%s<br>Total Pengeluaran: **%s**<br><br>💰 **SALDO & RINGKASAN**<br>Saldo saat ini: **%s**`,
								periodLabel, incomeListStr, formatRupiah(totalIncome), expenseListStr, formatRupiah(totalExpense), formatRupiah(saldo))
						}
					}
				} else {
					var lines []string
					var totalAmount float64
					for rows.Next() {
						var tanggal, deskripsi, kategori, rowTipe, status string
						var nominal float64
						if errScan := rows.Scan(&tanggal, &deskripsi, &kategori, &rowTipe, &nominal, &status); errScan == nil {
							dateStr := tanggal
							if len(tanggal) >= 10 {
								dateStr = tanggal[8:10] + "/" + tanggal[5:7]
							}

							catLabel, exists := catLabels[kategori]
							if !exists {
								catLabel = kategori
								if len(catLabel) > 0 {
									catLabel = strings.ToUpper(catLabel[0:1]) + catLabel[1:]
								}
							}

							statusSuffix := ""
							if status == "planned" {
								statusSuffix = " *(Planned)*"
							}

							line := fmt.Sprintf("• [%s] %s (%s): **%s**%s", dateStr, deskripsi, catLabel, formatRupiah(nominal), statusSuffix)
							lines = append(lines, line)
							totalAmount += nominal
						}
					}

					typeLabel := "pengeluaran"
					totalLabel := "Pengeluaran"
					header := "🧾 **Daftar Rincian Pengeluaran**"
					if tipe == "income" {
						typeLabel = "pemasukan"
						totalLabel = "Pemasukan"
						header = "🧾 **Daftar Rincian Pemasukan**"
					}

					if len(lines) == 0 {
						replyText = fmt.Sprintf("Tidak ada %s yang tercatat untuk periode **%s**. 🤷‍♂️", typeLabel, periodLabel)
					} else {
						if showDownloads {
							replyText = fmt.Sprintf(`%s (%s):<br><br>%s<br><br>**Total %s**: %s<br><br>📄 **UNDUH LAPORAN**<br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh PDF</a><br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh Excel</a><br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh Word</a>`,
								header, periodLabel, strings.Join(lines, "<br>"), totalLabel, formatRupiah(totalAmount), downloadPDF, downloadXLS, downloadWord)
						} else {
							replyText = fmt.Sprintf(`%s (%s):<br><br>%s<br><br>**Total %s**: %s`,
								header, periodLabel, strings.Join(lines, "<br>"), totalLabel, formatRupiah(totalAmount))
						}
					}
				}
			}

		default:
			replyText = "Maaf, bisa ulangi dengan format yang lebih jelas? 😊"
		}
	}

	// 3. Save Bot message to history
	_, err = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", replyText)
	if err != nil {
		log.Printf("Error saving bot chat: %v", err)
	}

	intentVal := analysis.Intent
	if isStateProcessed {
		if analysis.Intent == "HELP_TRIGGER" {
			intentVal = "HELP_TRIGGER"
		} else if analysis.Intent == "INTRO_TRIGGER" {
			intentVal = "INTRO_TRIGGER"
		} else {
			intentVal = "HELP_NAVIGATION"
		}
	}

	payload := map[string]string{
		"reply":  replyText,
		"intent": intentVal,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
}

func handleQueryAnswerGo(queryType string, userID string) string {
	switch queryType {
	case "SALDO_INQUIRY":
		var paidIncome, paidExpense, plannedExpense float64
		_ = DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'income' AND status = 'paid' AND user_id = ?", userID).Scan(&paidIncome)
		_ = DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'expense' AND status = 'paid' AND user_id = ?", userID).Scan(&paidExpense)
		_ = DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'expense' AND status = 'planned' AND user_id = ?", userID).Scan(&plannedExpense)

		saldo := paidIncome - paidExpense
		sisaAman := saldo - plannedExpense

		return fmt.Sprintf("Saldo: **%s**<br>Tagihan: **%s**<br>Sisa aman: **%s**",
			formatRupiah(saldo), formatRupiah(plannedExpense), formatRupiah(sisaAman))

	case "TAGIHAN_INQUIRY":
		rows, err := DB.Query("SELECT deskripsi, nominal FROM transactions WHERE tipe = 'expense' AND status = 'planned' AND user_id = ? ORDER BY tanggal ASC", userID)
		if err != nil {
			return "Gagal memuat tagihan. 🥺"
		}
		defer rows.Close()

		var lines []string
		var total float64
		for rows.Next() {
			var desc string
			var nominal float64
			_ = rows.Scan(&desc, &nominal)
			lines = append(lines, fmt.Sprintf("* %s: **%s**", desc, formatRupiah(nominal)))
			total += nominal
		}

		if len(lines) == 0 {
			return "Semua tagihan sudah beres 👍"
		}

		return fmt.Sprintf("Tagihan belum dibayar:<br>%s<br><br>Total: **%s**",
			strings.Join(lines, "<br>"), formatRupiah(total))

	case "TODAY_EXPENSE":
		todayStr := time.Now().Format("2006-01-02")
		rows, err := DB.Query("SELECT deskripsi, nominal FROM transactions WHERE tipe = 'expense' AND status = 'paid' AND tanggal = ? AND user_id = ? ORDER BY created_at DESC", todayStr, userID)
		if err != nil {
			return "Gagal memuat pengeluaran. 🥺"
		}
		defer rows.Close()

		var lines []string
		var total float64
		for rows.Next() {
			var desc string
			var nominal float64
			_ = rows.Scan(&desc, &nominal)
			lines = append(lines, fmt.Sprintf("* %s: **%s**", desc, formatRupiah(nominal)))
			total += nominal
		}

		if len(lines) == 0 {
			return "Belum ada pengeluaran dicatat untuk hari ini. 👍"
		}

		return fmt.Sprintf("Pengeluaran hari ini:<br>%s<br><br>Total: **%s**",
			strings.Join(lines, "<br>"), formatRupiah(total))

	case "MONTH_EXPENSE":
		currentMonth := int(time.Now().Month())
		currentYear := time.Now().Year()
		salaryDay := GetSalaryDay(userID)
		start, end := getMonthCycleBounds(currentYear, currentMonth, salaryDay)
		startStr := start.Format("2006-01-02")
		endStr := end.Format("2006-01-02")

		var total float64
		err := DB.QueryRow("SELECT COALESCE(SUM(nominal), 0) FROM transactions WHERE tipe = 'expense' AND status = 'paid' AND tanggal >= ? AND tanggal <= ? AND user_id = ?", startStr, endStr, userID).Scan(&total)
		if err != nil {
			return "Gagal memuat total bulanan. 🥺"
		}

		return fmt.Sprintf("Total pengeluaran bulan ini (%s s/d %s): **%s**", formatDateIndo(start), formatDateIndo(end), formatRupiah(total))

	case "REPORT_INQUIRY":
		currentMonth := int(time.Now().Month())
		currentYear := time.Now().Year()
		salaryDay := GetSalaryDay(userID)
		start, end := getMonthCycleBounds(currentYear, currentMonth, salaryDay)
		startStr := start.Format("2006-01-02")
		endStr := end.Format("2006-01-02")

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

		rows, err := DB.Query("SELECT tanggal, deskripsi, kategori, tipe, nominal, status FROM transactions WHERE tanggal >= ? AND tanggal <= ? AND user_id = ? ORDER BY tanggal ASC, created_at ASC", startStr, endStr, userID)
		if err != nil {
			log.Printf("DB Error querying transactions for report: %v", err)
			return "Gagal memuat data laporan dari database. 🥺"
		}
		defer rows.Close()

		var incomes []string
		var expenses []string
		var totalIncome, totalExpense float64

		for rows.Next() {
			var tanggal, deskripsi, kategori, tipe, status string
			var nominal float64
			if errScan := rows.Scan(&tanggal, &deskripsi, &kategori, &tipe, &nominal, &status); errScan == nil {
				dateStr := tanggal
				if len(tanggal) >= 10 {
					dateStr = tanggal[8:10] + "/" + tanggal[5:7]
				}

				catLabel, exists := catLabels[kategori]
				if !exists {
					catLabel = kategori
					if len(catLabel) > 0 {
						catLabel = strings.ToUpper(catLabel[0:1]) + catLabel[1:]
					}
				}

				statusSuffix := ""
				if status == "planned" {
					statusSuffix = " *(Planned)*"
				}

				line := fmt.Sprintf("• [%s] %s (%s): **%s**%s", dateStr, deskripsi, catLabel, formatRupiah(nominal), statusSuffix)
				if tipe == "income" {
					incomes = append(incomes, line)
					totalIncome += nominal
				} else {
					expenses = append(expenses, line)
					totalExpense += nominal
				}
			}
		}

		periodLabel := fmt.Sprintf("%s s/d %s", formatDateIndo(start), formatDateIndo(end))
		saldo := totalIncome - totalExpense

		incomeListStr := "Belum ada pemasukan"
		if len(incomes) > 0 {
			incomeListStr = strings.Join(incomes, "<br>")
		}

		expenseListStr := "Belum ada pengeluaran"
		if len(expenses) > 0 {
			expenseListStr = strings.Join(expenses, "<br>")
		}

		downloadPDF := fmt.Sprintf("/api/download/pdf?tipe=all&start_date=%s&end_date=%s&user_id=%s", startStr, endStr, userID)
		downloadXLS := fmt.Sprintf("/api/download/xls?tipe=all&start_date=%s&end_date=%s&user_id=%s", startStr, endStr, userID)
		downloadWord := fmt.Sprintf("/api/download/word?tipe=%s&start_date=%s&end_date=%s&user_id=%s", "all", startStr, endStr, userID)

		return fmt.Sprintf(`📊 **LAPORAN KEUANGAN BULAN INI**<br>Periode: **%s**<br><br>📥 **PEMASUKAN (INCOME)**<br>%s<br>Total Pemasukan: **%s**<br><br>📤 **PENGELUARAN (EXPENSE)**<br>%s<br>Total Pengeluaran: **%s**<br><br>💰 **SALDO & RINGKASAN**<br>Saldo saat ini: **%s**<br><br>📄 **UNDUH LAPORAN**<br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh PDF</a><br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh Excel</a><br>• <a href="%s" download class="text-[#00a884] font-semibold underline">Unduh Word</a>`,
			periodLabel, incomeListStr, formatRupiah(totalIncome), expenseListStr, formatRupiah(totalExpense), formatRupiah(saldo), downloadPDF, downloadXLS, downloadWord)
	}

	return "Maaf, terjadi kendala saat memproses query."
}

// ResetTransactions deletes all records and seeds default database data
func ResetTransactions(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	userID := getUserID(r)

	// Truncate tables for this user only
	_, err := DB.Exec("DELETE FROM transactions WHERE user_id = ?", userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = DB.Exec("DELETE FROM chat_history WHERE user_id = ?", userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = DB.Exec("DELETE FROM template_runs WHERE user_id = ?", userID)

	// Re-run seed for this user
	seedMockDataForUser(userID)
	ClearCategoryCache()

	notifMsg := "📢 **Notifikasi Dashboard**: Seluruh database transaksi dan riwayat chat Anda telah di-reset ke data demo bawaan melalui dashboard! 🔄"
	_, _ = DB.Exec("INSERT INTO chat_history (user_id, sender, message) VALUES (?, ?, ?)", userID, "bot", notifMsg)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Database reset and seeded successfully"})
}

// CORS helper middleware
func enableCors(w *http.ResponseWriter) {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
	(*w).Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
	(*w).Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
}

// GetPlannedKeywords retrieves all keywords from planned_keywords table
func GetPlannedKeywords(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	rows, err := DB.Query("SELECT id, keyword, created_at FROM planned_keywords ORDER BY keyword ASC")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []PlannedKeyword{}
	for rows.Next() {
		var kw PlannedKeyword
		if err := rows.Scan(&kw.ID, &kw.Keyword, &kw.CreatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		list = append(list, kw)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// CreatePlannedKeyword inserts a custom keyword into database
func CreatePlannedKeyword(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	var body struct {
		Keyword string `json:"keyword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	kwClean := strings.ToLower(strings.TrimSpace(body.Keyword))
	if kwClean == "" {
		http.Error(w, "Keyword cannot be empty", http.StatusBadRequest)
		return
	}

	res, err := DB.Exec("INSERT INTO planned_keywords (keyword) VALUES (?)", kwClean)
	if err != nil {
		if strings.Contains(err.Error(), "Duplicate entry") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"message": "Keyword already exists"})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	lastInsertId, _ := res.LastInsertId()

	newKw := PlannedKeyword{
		ID:        int(lastInsertId),
		Keyword:   kwClean,
		CreatedAt: time.Now(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(newKw)
}

// DeletePlannedKeyword deletes a keyword by ID
func DeletePlannedKeyword(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/planned-keywords/")
	if id == "" {
		http.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	_, err := DB.Exec("DELETE FROM planned_keywords WHERE id = ?", id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Keyword deleted successfully"})
}

// GetCategories retrieves all categories from database
func GetCategories(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	rows, err := DB.Query("SELECT id, label, tipe, keywords, created_at FROM categories ORDER BY label ASC")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []TransactionCategory{}
	for rows.Next() {
		var cat TransactionCategory
		if err := rows.Scan(&cat.ID, &cat.Label, &cat.Tipe, &cat.Keywords, &cat.CreatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		list = append(list, cat)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// GetDeleteTriggers retrieves all delete triggers from database
func GetDeleteTriggers(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	rows, err := DB.Query("SELECT keyword FROM delete_triggers ORDER BY keyword ASC")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []string{}
	for rows.Next() {
		var kw string
		if err := rows.Scan(&kw); err == nil {
			list = append(list, kw)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// GetSalaryDayTriggers retrieves all salary day triggers from database
func GetSalaryDayTriggers(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	rows, err := DB.Query("SELECT keyword FROM salary_day_triggers ORDER BY keyword ASC")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []string{}
	for rows.Next() {
		var kw string
		if err := rows.Scan(&kw); err == nil {
			list = append(list, kw)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// GetRecurringTemplates retrieves all recurring templates
func GetRecurringTemplates(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	userID := getUserID(r)
	rows, err := DB.Query("SELECT id, user_id, tipe, kategori, nominal, deskripsi, target_day, status, created_at FROM recurring_templates WHERE user_id = ? ORDER BY target_day ASC", userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	list := []RecurringTemplate{}
	for rows.Next() {
		var tmpl RecurringTemplate
		if err := rows.Scan(&tmpl.ID, &tmpl.UserID, &tmpl.Tipe, &tmpl.Kategori, &tmpl.Nominal, &tmpl.Deskripsi, &tmpl.TargetDay, &tmpl.Status, &tmpl.CreatedAt); err == nil {
			list = append(list, tmpl)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// DeleteRecurringTemplate deletes a template by ID
func DeleteRecurringTemplate(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/recurring-templates/")
	if id == "" {
		http.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	_, err := DB.Exec("DELETE FROM recurring_templates WHERE id = ?", id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Template deleted successfully"})
}

var (
	waSessionsMu sync.Mutex
	waSessions   = make(map[string]time.Time)
)

// WhatsAppWebhook handles Meta's verification (GET) and incoming messages (POST) from WhatsApp APIs (like api.co.id)
func WhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		// Verification from Meta / API providers
		mode := r.URL.Query().Get("hub.mode")
		token := r.URL.Query().Get("hub.verify_token")
		challenge := r.URL.Query().Get("hub.challenge")

		// Verify token (can be configured via env or constant)
		verifyToken := os.Getenv("WHATSAPP_VERIFY_TOKEN")
		if verifyToken == "" {
			verifyToken = "dompetku_secret_verify_token" // default secret token
		}

		if (mode == "subscribe" && token == verifyToken) || token == verifyToken {
			w.WriteHeader(http.StatusOK)
			if challenge != "" {
				w.Write([]byte(challenge))
			} else {
				w.Write([]byte("ok"))
			}
			log.Println("[WHATSAPP] Webhook verified successfully")
			return
		}

		// Fallback GET check (just echo back ok for simple API gateways verification)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
		return
	} else if r.Method == "POST" {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("[WHATSAPP] Error reading request body: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		senderNum, msgText, errParse := parseIncomingWebhook(bodyBytes)
		if errParse != nil {
			log.Printf("[WHATSAPP] Error parsing webhook: %v. Body: %s", errParse, string(bodyBytes))
			// Respond with 200 OK so Meta/gateway doesn't retry indefinitely
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("unsupported format"))
			return
		}

		log.Printf("[WHATSAPP] Received message from %s: %s", senderNum, msgText)

		if msgText != "" {
			trimmedMsg := strings.TrimSpace(msgText)
			hasPrefix := strings.HasPrefix(trimmedMsg, "^")

			waSessionsMu.Lock()
			lastActive, hasSession := waSessions[senderNum]
			now := time.Now()

			sessionValid := false
			if hasSession && now.Sub(lastActive) <= 5*time.Minute {
				sessionValid = true
			}

			if !sessionValid && !hasPrefix {
				waSessionsMu.Unlock()
				log.Printf("[WHATSAPP] Message ignored: no active session and prefix missing for sender %s", senderNum)
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("EVENT_RECEIVED"))
				return
			}

			// Clean message text by stripping the prefix
			processedMsg := trimmedMsg
			if hasPrefix {
				processedMsg = strings.TrimSpace(strings.TrimPrefix(trimmedMsg, "^"))
			}

			// Update session activity time
			waSessions[senderNum] = now
			waSessionsMu.Unlock()

			// If user sent ONLY the prefix "^", send activation notification and return
			if processedMsg == "" {
				reply := "Sesi WhatsApp aktif selama 5 menit ke depan! Silakan masukkan transaksi Anda. 😊"
				errSend := sendWhatsAppMessage(senderNum, reply)
				if errSend != nil {
					log.Printf("[WHATSAPP] Error sending activation reply to %s: %v", senderNum, errSend)
				}
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("EVENT_RECEIVED"))
				return
			}

			// Mock call to ProcessChat using httptest to reuse entire NLP engine and state machine logic
			wMock := httptest.NewRecorder()
			chatBody, _ := json.Marshal(map[string]string{
				"message": processedMsg,
				"user_id": senderNum,
			})
			rMock := httptest.NewRequest("POST", "/api/chat?user_id="+senderNum, bytes.NewReader(chatBody))
			rMock.Header.Set("Content-Type", "application/json")

			ProcessChat(wMock, rMock)

			// Read processed reply from response recorder
			var respPayload struct {
				Reply  string `json:"reply"`
				Intent string `json:"intent"`
			}
			respBytes := wMock.Body.Bytes()
			if errJson := json.Unmarshal(respBytes, &respPayload); errJson == nil && respPayload.Reply != "" {
				proto := "https"
				if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
					proto = p
				}
				baseURL := proto + "://" + r.Host
				if r.Host == "" {
					baseURL = "http://localhost:8085"
				}

				cleanedReply := cleanHTMLForWhatsApp(respPayload.Reply, baseURL)

				// Send reply back to the user via WhatsApp Send API
				errSend := sendWhatsAppMessage(senderNum, cleanedReply)
				if errSend != nil {
					log.Printf("[WHATSAPP] Error sending reply to %s: %v", senderNum, errSend)
				}
			} else {
				log.Printf("[WHATSAPP] ProcessChat returned empty or invalid response: %s", string(respBytes))
			}
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("EVENT_RECEIVED"))
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// parseIncomingWebhook robustly parses standard Meta webhook or flat simplified payloads
func parseIncomingWebhook(body []byte) (string, string, error) {
	// 1. Try to parse as Meta/WhatsApp standard nested webhook
	type MetaPayload struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Messages []struct {
						From string `json:"from"`
						Text struct {
							Body string `json:"body"`
						} `json:"text"`
						Type string `json:"type"`
					} `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}

	var meta MetaPayload
	if err := json.Unmarshal(body, &meta); err == nil && len(meta.Entry) > 0 && len(meta.Entry[0].Changes) > 0 && len(meta.Entry[0].Changes[0].Value.Messages) > 0 {
		msg := meta.Entry[0].Changes[0].Value.Messages[0]
		if msg.Type == "text" {
			return msg.From, msg.Text.Body, nil
		}
	}

	// 2. Try to parse as flat JSON maps (common for simplified WhatsApp gateways/api.co.id)
	var flat map[string]interface{}
	if err := json.Unmarshal(body, &flat); err == nil {
		// Try to find sender/from phone number
		var from string
		for _, key := range []string{"from", "sender", "phone", "phone_number", "customerId", "wa_id", "number"} {
			if val, ok := flat[key].(string); ok && val != "" {
				from = val
				break
			}
		}

		// Try to find message text
		var msgText string
		for _, key := range []string{"text", "message", "body", "msg", "content"} {
			if val, ok := flat[key].(string); ok && val != "" {
				msgText = val
				break
			}
		}

		// Also handle nested message object if any (e.g. {"message": {"text": "hello"}})
		if msgText == "" {
			if msgObj, ok := flat["message"].(map[string]interface{}); ok {
				for _, key := range []string{"text", "body", "content"} {
					if val, ok := msgObj[key].(string); ok && val != "" {
						msgText = val
						break
					}
				}
			}
		}

		if from != "" && msgText != "" {
			return from, msgText, nil
		}
	}

	// 3. Try to parse as api.co.id webhook event structure (nested in "data")
	type ApiCoIdEvent struct {
		EventType string `json:"event_type"`
		Data      struct {
			CustomerPhone string `json:"customer_phone"`
			Content       string `json:"content"`
			Direction     string `json:"direction"`
		} `json:"data"`
	}
	var apiEvent ApiCoIdEvent
	if err := json.Unmarshal(body, &apiEvent); err == nil && apiEvent.EventType == "message.received" {
		if apiEvent.Data.CustomerPhone != "" && apiEvent.Data.Content != "" {
			if apiEvent.Data.Direction == "inbound" || apiEvent.Data.Direction == "" {
				return apiEvent.Data.CustomerPhone, apiEvent.Data.Content, nil
			}
		}
	}

	return "", "", fmt.Errorf("unable to parse webhook payload: unknown format")
}

// sendWhatsAppMessage sends messages back via Meta Graph API or custom api.co.id endpoints
func sendWhatsAppMessage(toPhone, messageText string) error {
	apiKey := os.Getenv("WHATSAPP_API_KEY")
	if apiKey == "" {
		apiKey = "apicoid_live_BUrDwTcJrwDyxxPM931fRdiIHTCPzQMJwDf0K2v4MbQ" // default key from user's screen
	}
	fmt.Println("APikey", apiKey)

	sendURL := os.Getenv("WHATSAPP_SEND_URL")
	if sendURL == "" {
		// Default to api.co.id conversation send endpoint
		sendURL = "https://chat.api.co.id/api/v1/public/messages/send"
	} else {
		// Replace placeholder variables if configured in env
		sendURL = strings.ReplaceAll(sendURL, ":customerId", toPhone)
		sendURL = strings.ReplaceAll(sendURL, "{phone}", toPhone)
	}

	// Determine the payload format based on the URL
	var requestBody []byte
	var err error

	phoneId := os.Getenv("WHATSAPP_PHONE_NUMBER_ID")
	if phoneId == "" {
		phoneId = "cmq6tnmze02ezphspujxbfivl"
	}

	if strings.Contains(sendURL, "/api/v1/public/messages/send") {
		// New api.co.id format
		bodyMap := map[string]interface{}{
			"phone_number":             toPhone,
			"channel":                  "whatsapp",
			"message_type":             "text",
			"content":                  messageText,
			"whatsapp_phone_number_id": phoneId,
		}
		requestBody, err = json.Marshal(bodyMap)
	} else {
		// Legacy / alternative gateway format
		bodyMap := map[string]interface{}{
			"text":    messageText,
			"message": messageText,
			"body":    messageText,
			"to":      toPhone,
		}
		requestBody, err = json.Marshal(bodyMap)
	}

	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", sendURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("[WHATSAPP] Message successfully sent to %s", toPhone)
	return nil
}
