package main

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ParseResult holds the detected intent and relevant metadata
type ParseResult struct {
	Intent string                 `json:"intent"`
	Data   map[string]interface{} `json:"data"`
}

// ParseNominal extracts numbers with suffixes like '20rb', '1.5jt', '500k', '5 juta'
func ParseNominal(text string) (float64, bool) {
	cleaned := strings.ToLower(text)
	cleaned = strings.ReplaceAll(cleaned, "rp", "")
	
	// Pattern 1: Number with suffix (e.g. 1.5jt, 20rb, 500k, 5 juta)
	// Match float/int followed by whitespace/none and the suffix
	suffixRegex := regexp.MustCompile(`(\d+(?:[\.,]\d+)?)\s*(juta|jt|ribu|rb|k)`)
	suffixMatches := suffixRegex.FindStringSubmatch(cleaned)
	
	if len(suffixMatches) == 3 {
		numStr := strings.ReplaceAll(suffixMatches[1], ",", ".")
		num, err := strconv.ParseFloat(numStr, 64)
		if err == nil {
			suffix := suffixMatches[2]
			switch suffix {
			case "juta", "jt":
				return num * 1000000, true
			case "ribu", "rb", "k":
				return num * 1000, true
			}
		}
	}
	
	// Pattern 2: Raw number (e.g. 150.000 or 150000)
	plainRegex := regexp.MustCompile(`(\d+(?:[\.,]\d+)*)`)
	plainMatches := plainRegex.FindStringSubmatch(cleaned)
	if len(plainMatches) == 2 {
		rawNum := plainMatches[1]
		
		// If both dot and comma exist, it's typically thousands.decimals (e.g. 1.500.000,00)
		if strings.Contains(rawNum, ".") && strings.Contains(rawNum, ",") {
			rawNum = strings.ReplaceAll(rawNum, ".", "")
			rawNum = strings.ReplaceAll(rawNum, ",", ".")
		} else if strings.Contains(rawNum, ".") {
			// Standard Indonesian: 300.000 (ends with 3 digits) vs 1.5 (decimal)
			parts := strings.Split(rawNum, ".")
			if len(parts[len(parts)-1]) == 3 {
				rawNum = strings.ReplaceAll(rawNum, ".", "")
			}
		} else if strings.Contains(rawNum, ",") {
			parts := strings.Split(rawNum, ",")
			if len(parts[len(parts)-1]) == 3 {
				rawNum = strings.ReplaceAll(rawNum, ",", "")
			} else {
				rawNum = strings.ReplaceAll(rawNum, ",", ".")
			}
		}
		
		num, err := strconv.ParseFloat(rawNum, 64)
		if err == nil {
			return num, true
		}
	}
	
	return 0, false
}

// CachedCategory stores category ID, type and expanded keyword permutations in memory
type CachedCategory struct {
	ID       string
	Tipe     string
	Keywords []string
}

var (
	categoryCache   []CachedCategory
	categoryCacheMu sync.RWMutex
)

// ClearCategoryCache clears the category cache so it reloads on next match
func ClearCategoryCache() {
	categoryCacheMu.Lock()
	categoryCache = nil
	categoryCacheMu.Unlock()
}

// hasWordBoundary checks if kw exists in text with proper word boundaries
func hasWordBoundary(text, kw string) bool {
	if kw == "" {
		return false
	}
	start := 0
	for {
		idx := strings.Index(text[start:], kw)
		if idx == -1 {
			return false
		}
		actualIdx := start + idx
		// Check start boundary
		startOk := true
		if actualIdx > 0 {
			startOk = !isAlphanumeric(text[actualIdx-1])
		}
		// Check end boundary
		endOk := true
		endIdx := actualIdx + len(kw)
		if endIdx < len(text) {
			endOk = !isAlphanumeric(text[endIdx])
		}
		if startOk && endOk {
			return true
		}
		start = actualIdx + 1
		if start >= len(text) {
			return false
		}
	}
}

func isAlphanumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// InferCategory analyzes text to guess category using word boundary checks
func InferCategory(text string, txType string) string {
	textLower := strings.ToLower(text)

	categoryCacheMu.RLock()
	cacheExists := categoryCache != nil
	categoryCacheMu.RUnlock()

	if !cacheExists {
		categoryCacheMu.Lock()
		if categoryCache == nil {
			var entries []CachedCategory
			if DB != nil {
				rows, err := DB.Query("SELECT id, tipe, keywords FROM categories")
				if err == nil {
					defer rows.Close()
					for rows.Next() {
						var id, tipe, kwStr string
						if err := rows.Scan(&id, &tipe, &kwStr); err == nil {
							var kwList []string
							for _, k := range strings.Split(kwStr, ",") {
								kClean := strings.TrimSpace(strings.ToLower(k))
								if kClean != "" {
									kwList = append(kwList, kClean)
								}
							}
							
							// Expand keywords in memory with modifiers
							expandedKws := make(map[string]bool)
							for _, kw := range kwList {
								expandedKws[kw] = true
								for _, mod := range CategoryModifiers {
									expandedKws[kw+" "+mod] = true
									expandedKws[mod+" "+kw] = true
								}
							}
							
							var finalKws []string
							for k := range expandedKws {
								finalKws = append(finalKws, k)
							}
							
							entries = append(entries, CachedCategory{ID: id, Tipe: tipe, Keywords: finalKws})
						}
					}
				}
			}
			
			// Fallback if DB query fails or returns no categories
			if len(entries) == 0 {
				defaultFallback := []struct {
					id   string
					tipe string
					kws  []string
				}{
					// Expense
					{"makan", "expense", []string{"sayur", "makan", "bakso", "kopi", "beras", "jajan", "resto", "cafe", "cemilan", "warteg", "indomie", "susu", "roti", "daging", "dapur"}},
					{"listrik", "expense", []string{"listrik", "pln", "token", "daya", "air", "pdam", "gas", "utilitas"}},
					{"transport", "expense", []string{"bensin", "ojek", "gojek", "grab", "tol", "parkir", "transport", "ojol", "bus", "kereta", "service", "oli", "ban", "solar", "pertalite", "pertamax"}},
					{"internet", "expense", []string{"wifi", "internet", "pulsa", "kuota", "telkomsel", "indihome", "xl", "tri", "indosat", "smartfren", "netflix", "spotify"}},
					{"belanja", "expense", []string{"baju", "celana", "sepatu", "makeup", "skincare", "tokopedia", "shopee", "lazada", "mall", "tas", "aksesoris", "hobi"}},
					{"kesehatan", "expense", []string{"obat", "dokter", "klinik", "rs", "apotek", "vitamin", "bpjs", "periksa", "sakit"}},
					{"pendidikan", "expense", []string{"sekolah", "buku", "kuliah", "spp", "kursus", "les", "alat tulis"}},
					{"tempat tinggal", "expense", []string{"kos", "kontrakan", "sewa", "apartemen", "cicilan rumah", "perumahan", "rt"}},
					// Income
					{"gaji", "income", []string{"gaji", "salary"}},
					{"bonus", "income", []string{"bonus", "thr", "hadiah"}},
					{"sampingan", "income", []string{"sampingan", "proyek", "freelance"}},
				}
				for _, fb := range defaultFallback {
					expandedKws := make(map[string]bool)
					for _, kw := range fb.kws {
						expandedKws[kw] = true
						for _, mod := range CategoryModifiers {
							expandedKws[kw+" "+mod] = true
							expandedKws[mod+" "+kw] = true
						}
					}
					var finalKws []string
					for k := range expandedKws {
						finalKws = append(finalKws, k)
					}
					entries = append(entries, CachedCategory{ID: fb.id, Tipe: fb.tipe, Keywords: finalKws})
				}
			}
			categoryCache = entries
		}
		categoryCacheMu.Unlock()
	}

	categoryCacheMu.RLock()
	defer categoryCacheMu.RUnlock()

	for _, entry := range categoryCache {
		if entry.Tipe != txType {
			continue
		}
		for _, kw := range entry.Keywords {
			if hasWordBoundary(textLower, kw) {
				return entry.ID
			}
		}
	}

	return "lainnya"
}

var indoMonths = map[string]int{
	"januari":   1,
	"februari":  2,
	"maret":     3,
	"april":     4,
	"mei":       5,
	"juni":      6,
	"juli":      7,
	"agustus":   8,
	"september": 9,
	"oktober":   10,
	"november":  11,
	"desember":  12,
	"jan":       1,
	"feb":       2,
	"mar":       3,
	"mrt":       3,
	"apr":       4,
	"jun":       6,
	"jul":       7,
	"agu":       8,
	"agt":       8,
	"agus":      8,
	"agst":      8,
	"sep":       9,
	"okt":       10,
	"nov":       11,
	"des":       12,
}

func parseMonthAndYear(text string) (int, int) {
	now := time.Now()
	month := int(now.Month())
	year := now.Year()

	textLower := strings.ToLower(text)

	foundMonth := false
	for name, mVal := range indoMonths {
		if hasWordBoundary(textLower, name) {
			month = mVal
			foundMonth = true
			break
		}
	}

	if !foundMonth {
		if strings.Contains(textLower, "bulan lalu") {
			prev := now.AddDate(0, -1, 0)
			month = int(prev.Month())
			year = prev.Year()
			foundMonth = true
		}
	}

	yearRegex := regexp.MustCompile(`\b(20\d{2})\b`)
	yearMatches := yearRegex.FindStringSubmatch(textLower)
	if len(yearMatches) == 2 {
		if y, err := strconv.Atoi(yearMatches[1]); err == nil {
			year = y
		}
	}

	return month, year
}

func parseIndonesianDate(text string, defaultMonth int, defaultYear int) (time.Time, bool) {
	text = strings.ToLower(text)
	
	// Find year
	year := defaultYear
	yearRegex := regexp.MustCompile(`\b(20\d{2})\b`)
	yearMatches := yearRegex.FindStringSubmatch(text)
	if len(yearMatches) == 2 {
		if y, err := strconv.Atoi(yearMatches[1]); err == nil {
			year = y
		}
	}
	
	// Find month
	month := defaultMonth
	for name, mVal := range indoMonths {
		if hasWordBoundary(text, name) {
			month = mVal
			break
		}
	}
	
	// Also support numeric month if format is dd/mm or dd-mm or dd/mm/yyyy
	dateSlashRegex := regexp.MustCompile(`\b(\d{1,2})[\/-](\d{1,2})\b`)
	dateSlashMatches := dateSlashRegex.FindStringSubmatch(text)
	
	var day int
	if len(dateSlashMatches) == 3 {
		d, errD := strconv.Atoi(dateSlashMatches[1])
		m, errM := strconv.Atoi(dateSlashMatches[2])
		if errD == nil && errM == nil && d >= 1 && d <= 31 && m >= 1 && m <= 12 {
			day = d
			month = m
		}
	} else {
		// Just look for the first isolated number as day
		dayRegex := regexp.MustCompile(`\b(\d{1,2})\b`)
		dayMatches := dayRegex.FindAllStringSubmatch(text, -1)
		for _, dm := range dayMatches {
			val, err := strconv.Atoi(dm[1])
			if err == nil && val >= 1 && val <= 31 && val != year {
				day = val
				break
			}
		}
	}
	
	if day == 0 {
		return time.Time{}, false
	}
	
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return t, true
}

// ProcessUserInput processes incoming message and returns structured intent
func ProcessUserInput(messageText string) ParseResult {
	textLower := strings.TrimSpace(strings.ToLower(messageText))

	// 0. QUERY SALARY DAY CHECK (salary_day_triggers)
	if DB != nil {
		cleanTextForMatch := textLower
		cleanTextForMatch = strings.ReplaceAll(cleanTextForMatch, "?", "")
		cleanTextForMatch = strings.ReplaceAll(cleanTextForMatch, "!", "")
		cleanTextForMatch = strings.ReplaceAll(cleanTextForMatch, ".", "")
		cleanTextForMatch = strings.ReplaceAll(cleanTextForMatch, ",", "")
		cleanTextForMatch = strings.TrimSpace(cleanTextForMatch)

		var count int
		err := DB.QueryRow("SELECT COUNT(*) FROM salary_day_triggers WHERE ? = keyword OR ? LIKE CONCAT('% ', keyword, ' %') OR ? LIKE CONCAT(keyword, ' %') OR ? LIKE CONCAT('% ', keyword)", cleanTextForMatch, cleanTextForMatch, cleanTextForMatch, cleanTextForMatch).Scan(&count)
		if err == nil && count > 0 {
			return ParseResult{
				Intent: "QUERY_SALARY_DAY",
				Data:   map[string]interface{}{},
			}
		}
	}

	// 0. DELETE TEMPLATE CHECK
	var deletePrefix string
	for _, pfx := range []string{"hapus template ", "delete template ", "remove template "} {
		if strings.HasPrefix(textLower, pfx) {
			deletePrefix = pfx
			break
		}
	}
	if deletePrefix != "" {
		targetDesc := strings.TrimSpace(messageText[len(deletePrefix):])
		if targetDesc != "" {
			return ParseResult{
				Intent: "DELETE_TEMPLATE",
				Data: map[string]interface{}{
					"deskripsi": targetDesc,
				},
			}
		}
	}

	// 0. UPDATE TEMPLATE CHECK
	var updatePrefix string
	for _, pfx := range []string{"ubah template ", "update template ", "ganti template ", "edit template "} {
		if strings.HasPrefix(textLower, pfx) {
			updatePrefix = pfx
			break
		}
	}
	if updatePrefix != "" {
		rem := strings.TrimSpace(messageText[len(updatePrefix):])
		remLower := strings.ToLower(rem)
		
		// Split by " menjadi " or " jadi " or " to " or " = "
		var targetDesc, details string
		delimIdx := -1
		delims := []string{" menjadi ", " jadi ", " to ", " = "}
		for _, delim := range delims {
			idx := strings.Index(remLower, delim)
			if idx != -1 {
				delimIdx = idx
				targetDesc = strings.TrimSpace(rem[:idx])
				details = strings.TrimSpace(rem[idx+len(delim):])
				break
			}
		}
		
		if delimIdx != -1 && targetDesc != "" && details != "" {
			nominal, okNom := ParseNominal(details)
			if okNom {
				dayRegex := regexp.MustCompile(`(?i)\b(?:tiap|setiap|tanggal|tgl)\s*(\d{1,2})\b`)
				dayMatches := dayRegex.FindStringSubmatch(details)
				dayVal := 1
				hasDay := false
				if len(dayMatches) == 2 {
					if d, err := strconv.Atoi(dayMatches[1]); err == nil && d >= 1 && d <= 31 {
						dayVal = d
						hasDay = true
					}
				}
				
				return ParseResult{
					Intent: "UPDATE_TEMPLATE",
					Data: map[string]interface{}{
						"deskripsi_target": targetDesc,
						"nominal":          nominal,
						"day":              dayVal,
						"has_day":          hasDay,
					},
				}
			}
		}
	}

	// 0. CREATE TEMPLATE CHECK
	if strings.HasPrefix(textLower, "tambah template:") || strings.HasPrefix(textLower, "tambah templates:") {
		lines := strings.Split(messageText, "\n")
		var templates []map[string]interface{}
		
		dayRegex := regexp.MustCompile(`\b(?:tiap|setiap|tanggal|tgl)\s*(\d{1,2})\b`)
		
		for _, line := range lines {
			lineClean := strings.TrimSpace(line)
			lineLower := strings.ToLower(lineClean)
			
			if strings.HasPrefix(lineLower, "tambah template:") || strings.HasPrefix(lineLower, "tambah templates:") {
				continue
			}
			if lineClean == "" {
				continue
			}
			
			if strings.HasPrefix(lineClean, "-") || strings.HasPrefix(lineClean, "*") {
				lineClean = strings.TrimSpace(lineClean[1:])
				lineLower = strings.ToLower(lineClean)
			}
			
			nominal, okNom := ParseNominal(lineClean)
			if !okNom {
				continue
			}
			
			dayVal := 1
			dayMatches := dayRegex.FindStringSubmatch(lineLower)
			var dayTriggerStr string
			if len(dayMatches) == 2 {
				if d, err := strconv.Atoi(dayMatches[1]); err == nil && d >= 1 && d <= 31 {
					dayVal = d
					dayTriggerStr = dayMatches[0]
				}
			}
			
			tipe := "expense"
			if strings.Contains(lineLower, "gaji") || strings.Contains(lineLower, "bonus") || strings.Contains(lineLower, "thr") || strings.Contains(lineLower, "sampingan") || strings.Contains(lineLower, "pemasukan") || strings.Contains(lineLower, "income") {
				tipe = "income"
			}
			
			category := InferCategory(lineClean, tipe)
			
			descClean := lineClean
			if dayTriggerStr != "" {
				reDay := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(dayTriggerStr))
				descClean = reDay.ReplaceAllString(descClean, "")
			}
			
			nomRegex := regexp.MustCompile(`\b\d+(?:[\.,]\d+)?\s*(?:juta|jt|ribu|rb|k)?\b`)
			descClean = nomRegex.ReplaceAllString(descClean, "")
			descClean = regexp.MustCompile(`(?i)\brp\b`).ReplaceAllString(descClean, "")
			
			descClean = strings.TrimSpace(descClean)
			descClean = regexp.MustCompile(`\s+`).ReplaceAllString(descClean, " ")
			
			if descClean == "" {
				descClean = category
			}
			
			if len(descClean) > 0 {
				descClean = strings.ToUpper(descClean[0:1]) + descClean[1:]
			}
			
			templates = append(templates, map[string]interface{}{
				"tipe":      tipe,
				"kategori":  category,
				"nominal":   nominal,
				"deskripsi": descClean,
				"day":       dayVal,
				"status":    "planned",
			})
		}
		
		if len(templates) > 0 {
			return ParseResult{
				Intent: "CREATE_TEMPLATES",
				Data: map[string]interface{}{
					"templates": templates,
				},
			}
		}
	}

	// 0. TRIGGER_RESET_DATA CHECK
	var resetKeywords []string
	if DB != nil {
		rows, err := DB.Query("SELECT keyword FROM delete_triggers")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var kw string
				if err := rows.Scan(&kw); err == nil {
					resetKeywords = append(resetKeywords, strings.ToLower(strings.TrimSpace(kw)))
				}
			}
		}
	}
	if len(resetKeywords) == 0 {
		resetKeywords = []string{"hapus semua data", "hapus semua transaksi", "reset data", "clear data", "kosongkan data", "kosongkan database", "bersihkan data", "bersihkan database", "reset database"}
	}

	isResetMatch := false
	for _, rkw := range resetKeywords {
		if strings.Contains(textLower, rkw) {
			isResetMatch = true
			break
		}
	}
	// Fallback spelling tolerance for reset triggers
	if !isResetMatch && (strings.Contains(textLower, "hapus") || strings.Contains(textLower, "reset") || strings.Contains(textLower, "clear") || strings.Contains(textLower, "bersih")) {
		if strings.Contains(textLower, "pemasuk") || strings.Contains(textLower, "gaji") || strings.Contains(textLower, "income") {
			isResetMatch = true
		} else if strings.Contains(textLower, "pengeluar") || strings.Contains(textLower, "keluar") || strings.Contains(textLower, "expense") || strings.Contains(textLower, "belanja") {
			isResetMatch = true
		} else if strings.Contains(textLower, "semua") || strings.Contains(textLower, "data") || strings.Contains(textLower, "transaksi") || strings.Contains(textLower, "db") || strings.Contains(textLower, "database") {
			isResetMatch = true
		}
	}

	if isResetMatch {
		tipe := "all" // Default is all if not specified
		if strings.Contains(textLower, "pemasuk") || strings.Contains(textLower, "gaji") || strings.Contains(textLower, "income") {
			tipe = "income"
		} else if strings.Contains(textLower, "pengeluar") || strings.Contains(textLower, "keluar") || strings.Contains(textLower, "expense") || strings.Contains(textLower, "belanja") {
			tipe = "expense"
		}
		return ParseResult{
			Intent: "TRIGGER_RESET_DATA",
			Data: map[string]interface{}{
				"tipe": tipe,
			},
		}
	}

	// 0. SET SALARY DAY
	if strings.Contains(textLower, "gaji") || strings.Contains(textLower, "gajian") {
		if strings.Contains(textLower, "set") || strings.Contains(textLower, "tanggal") || strings.Contains(textLower, "tgl") || strings.Contains(textLower, "tiap") || strings.Contains(textLower, "setiap") {
			dayRegex := regexp.MustCompile(`\b(\d{1,2})\b`)
			matches := dayRegex.FindAllStringSubmatch(textLower, -1)
			for _, m := range matches {
				if dVal, err := strconv.Atoi(m[1]); err == nil && dVal >= 1 && dVal <= 31 {
					return ParseResult{
						Intent: "SET_SALARY_DAY",
						Data: map[string]interface{}{
							"day": dVal,
						},
					}
				}
			}
		}
	}
	
	// 0. INTRO TRIGGER CHECK
	if DB != nil {
		var count int
		err := DB.QueryRow("SELECT COUNT(*) FROM intro_triggers WHERE ? = keyword OR ? LIKE CONCAT('% ', keyword, ' %') OR ? LIKE CONCAT(keyword, ' %') OR ? LIKE CONCAT('% ', keyword)", textLower, textLower, textLower, textLower).Scan(&count)
		if err == nil && count > 0 {
			return ParseResult{
				Intent: "INTRO_TRIGGER",
				Data:   map[string]interface{}{},
			}
		}
	}

	// 0. HELP TRIGGER CHECK
	if DB != nil {
		var count int
		err := DB.QueryRow("SELECT COUNT(*) FROM help_triggers WHERE ? = keyword OR ? LIKE CONCAT('% ', keyword, ' %') OR ? LIKE CONCAT(keyword, ' %') OR ? LIKE CONCAT('% ', keyword)", textLower, textLower, textLower, textLower).Scan(&count)
		if err == nil && count > 0 {
			return ParseResult{
				Intent: "HELP_TRIGGER",
				Data:   map[string]interface{}{},
			}
		}
	}

	// Parse custom date range
	rangeSeparators := []string{" sampai ", " sd ", " s/d ", " hingga ", " - "}
	var startDate, endDate time.Time
	var hasRange bool
	for _, sep := range rangeSeparators {
		if idx := strings.Index(textLower, sep); idx != -1 {
			left := textLower[:idx]
			right := textLower[idx+len(sep):]
			
			tRight, okRight := parseIndonesianDate(right, int(time.Now().Month()), time.Now().Year())
			if okRight {
				tLeft, okLeft := parseIndonesianDate(left, int(tRight.Month()), tRight.Year())
				if okLeft {
					startDate = tLeft
					endDate = tRight
					hasRange = true
					break
				}
			}
		}
	}

	// Detect LIST_TRANSACTIONS_BY_MONTH
	isListTx := false
	txTipe := "expense"
	listKeywords := []string{"list", "rincian", "daftar", "detail", "tampilkan", "laporan", "report", "rekap"}
	hasListKeyword := false
	for _, kw := range listKeywords {
		if strings.Contains(textLower, kw) {
			hasListKeyword = true
			break
		}
	}

	// Also treat as list if it's explicitly "pemasukan" or "pengeluaran" and has NO nominal
	_, hasNominalForList := ParseNominal(textLower)
	isExplicitTypeQuery := !hasNominalForList && (strings.Contains(textLower, "pemasukan") || strings.Contains(textLower, "pengeluaran"))

	if hasListKeyword || isExplicitTypeQuery {
		if strings.Contains(textLower, "pengeluaran") {
			isListTx = true
			txTipe = "expense"
		} else if strings.Contains(textLower, "pemasukan") {
			isListTx = true
			txTipe = "income"
		} else {
			isListTx = true
			txTipe = "all"
		}
	}

	if isListTx {
		if hasRange {
			format := ""
			if strings.Contains(textLower, "pdf") {
				format = "pdf"
			} else if strings.Contains(textLower, "xls") || strings.Contains(textLower, "excel") || strings.Contains(textLower, "xlsx") {
				format = "xls"
			} else if strings.Contains(textLower, "word") || strings.Contains(textLower, "doc") || strings.Contains(textLower, "docx") {
				format = "word"
			}

			if format != "" {
				return ParseResult{
					Intent: "DOWNLOAD_TRANSACTIONS_RANGE",
					Data: map[string]interface{}{
						"tipe":       txTipe,
						"start_date": startDate.Format("2006-01-02"),
						"end_date":   endDate.Format("2006-01-02"),
						"format":     format,
					},
				}
			}

			return ParseResult{
				Intent: "LIST_TRANSACTIONS_RANGE",
				Data: map[string]interface{}{
					"tipe":       txTipe,
					"start_date": startDate.Format("2006-01-02"),
					"end_date":   endDate.Format("2006-01-02"),
				},
			}
		}

		month, year := parseMonthAndYear(textLower)
		
		format := ""
		if strings.Contains(textLower, "pdf") {
			format = "pdf"
		} else if strings.Contains(textLower, "xls") || strings.Contains(textLower, "excel") || strings.Contains(textLower, "xlsx") {
			format = "xls"
		} else if strings.Contains(textLower, "word") || strings.Contains(textLower, "doc") || strings.Contains(textLower, "docx") {
			format = "word"
		}
		
		if format != "" {
			return ParseResult{
				Intent: "DOWNLOAD_TRANSACTIONS",
				Data: map[string]interface{}{
					"tipe":   txTipe,
					"month":  month,
					"year":   year,
					"format": format,
				},
			}
		}

		return ParseResult{
			Intent: "LIST_TRANSACTIONS_BY_MONTH",
			Data: map[string]interface{}{
				"tipe":  txTipe,
				"month": month,
				"year":  year,
			},
		}
	}
	
	// A. Chat-based CRUD Commands
	
	// 1. TAMBAH KATEGORI (Create Category)
	// Format: tambah kategori [id] [label] [tipe]
	if strings.HasPrefix(textLower, "tambah kategori ") {
		content := strings.TrimSpace(messageText[len("tambah kategori "):])
		parts := strings.Fields(content)
		if len(parts) >= 2 {
			id := strings.ToLower(parts[0])
			tipe := strings.ToLower(parts[len(parts)-1])
			if tipe == "expense" || tipe == "income" {
				label := strings.Join(parts[1:len(parts)-1], " ")
				if label == "" {
					label = id
					if len(label) > 0 {
						label = strings.ToUpper(label[0:1]) + label[1:]
					}
				}
				return ParseResult{
					Intent: "CREATE_CATEGORY",
					Data: map[string]interface{}{
						"id":    id,
						"label": label,
						"tipe":  tipe,
					},
				}
			}
		}
	}

	// 2. DAFTAR KATEGORI (Read/List Categories)
	// Format: daftar kategori / lihat kategori / list kategori
	if textLower == "daftar kategori" || textLower == "lihat kategori" || textLower == "list kategori" {
		return ParseResult{
			Intent: "LIST_CATEGORIES",
			Data:   map[string]interface{}{},
		}
	}

	// 3. HAPUS KATEGORI (Delete Category)
	// Format: hapus kategori [id]
	if strings.HasPrefix(textLower, "hapus kategori ") {
		id := strings.TrimSpace(strings.ToLower(messageText[len("hapus kategori "):]))
		if id != "" {
			return ParseResult{
				Intent: "DELETE_CATEGORY",
				Data: map[string]interface{}{
					"id": id,
				},
			}
		}
	}

	// 4. TAMBAH KEYWORD (Add Keywords)
	// Format: tambah keyword [kategori_id] [keywords...]
	var isAddKeywords bool
	var kwContent string
	if strings.HasPrefix(textLower, "tambah keyword ") {
		isAddKeywords = true
		kwContent = messageText[len("tambah keyword "):]
	} else if strings.HasPrefix(textLower, "tambah keywords ") {
		isAddKeywords = true
		kwContent = messageText[len("tambah keywords "):]
	}

	if isAddKeywords && kwContent != "" {
		parts := strings.Fields(kwContent)
		if len(parts) >= 2 {
			catID := strings.ToLower(parts[0])
			kwPart := strings.Join(parts[1:], " ")
			return ParseResult{
				Intent: "ADD_KEYWORDS",
				Data: map[string]interface{}{
					"category_id": catID,
					"keywords":    kwPart,
				},
			}
		}
	}

	// 5. HAPUS KEYWORD (Delete Keywords)
	// Format: hapus keyword [kategori_id] [keywords...]
	var isDelKeywords bool
	if strings.HasPrefix(textLower, "hapus keyword ") {
		isDelKeywords = true
		kwContent = messageText[len("hapus keyword "):]
	} else if strings.HasPrefix(textLower, "hapus keywords ") {
		isDelKeywords = true
		kwContent = messageText[len("hapus keywords "):]
	}

	if isDelKeywords && kwContent != "" {
		parts := strings.Fields(kwContent)
		if len(parts) >= 2 {
			catID := strings.ToLower(parts[0])
			kwPart := strings.Join(parts[1:], " ")
			return ParseResult{
				Intent: "DELETE_KEYWORDS",
				Data: map[string]interface{}{
					"category_id": catID,
					"keywords":    kwPart,
				},
			}
		}
	}

	// 6. DAFTAR KEYWORD (Read/List Keywords)
	// Format: daftar keyword [kategori_id] / lihat keyword [kategori_id]
	var isListKeywords bool
	if strings.HasPrefix(textLower, "daftar keyword ") {
		isListKeywords = true
		kwContent = messageText[len("daftar keyword "):]
	} else if strings.HasPrefix(textLower, "daftar keywords ") {
		isListKeywords = true
		kwContent = messageText[len("daftar keywords "):]
	} else if strings.HasPrefix(textLower, "lihat keyword ") {
		isListKeywords = true
		kwContent = messageText[len("lihat keyword "):]
	} else if strings.HasPrefix(textLower, "lihat keywords ") {
		isListKeywords = true
		kwContent = messageText[len("lihat keywords "):]
	}

	if isListKeywords && kwContent != "" {
		catID := strings.TrimSpace(strings.ToLower(kwContent))
		if catID != "" {
			return ParseResult{
				Intent: "LIST_KEYWORDS",
				Data: map[string]interface{}{
					"category_id": catID,
				},
			}
		}
	}

	// 0. DELETE TRANSAKSI (e.g. "hapus bensin" or "batal")
	deleteKeywords := []string{"hapus", "batal", "cancel", "delete"}
	isDeleteIntent := false
	for _, kw := range deleteKeywords {
		if strings.Contains(textLower, kw) {
			isDeleteIntent = true
			break
		}
	}
	
	if isDeleteIntent {
		target := "last"
		keyword := ""
		tipe := "all"
		
		cleanMsg := textLower
		for _, kw := range deleteKeywords {
			cleanMsg = strings.ReplaceAll(cleanMsg, kw, "")
		}
		cleanMsg = strings.ReplaceAll(cleanMsg, "transaksi", "")
		cleanMsg = strings.ReplaceAll(cleanMsg, "terakhir", "")
		cleanMsg = strings.ReplaceAll(cleanMsg, "baru", "")
		cleanMsg = strings.TrimSpace(cleanMsg)
		
		if cleanMsg != "" {
			target = "keyword"
			if strings.Contains(cleanMsg, "pengeluaran") || strings.Contains(cleanMsg, "expense") {
				tipe = "expense"
				cleanMsg = strings.ReplaceAll(cleanMsg, "pengeluaran", "")
				cleanMsg = strings.ReplaceAll(cleanMsg, "expense", "")
			} else if strings.Contains(cleanMsg, "pemasukan") || strings.Contains(cleanMsg, "income") {
				tipe = "income"
				cleanMsg = strings.ReplaceAll(cleanMsg, "pemasukan", "")
				cleanMsg = strings.ReplaceAll(cleanMsg, "income", "")
			}
			cleanMsg = strings.TrimSpace(cleanMsg)
			keyword = cleanMsg
		}
		
		return ParseResult{
			Intent: "DELETE_TRANSAKSI",
			Data: map[string]interface{}{
				"target":  target,
				"keyword": keyword,
				"tipe":    tipe,
			},
		}
	}

	_, hasNominal := ParseNominal(textLower)
	
	// 1. INPUT TRANSAKSI (with parsed nominal)
	if hasNominal {
		nominal, _ := ParseNominal(messageText)
		
		// Detect type (income vs expense)
		incomeKeywords := []string{"gaji", "pemasukan", "income", "transfer masuk", "dapat duit", "bonus", "sampingan", "untung", "angpao", "salary", "thr", "uang masuk", "masuk"}
		tipe := "expense"
		for _, kw := range incomeKeywords {
			if strings.Contains(textLower, kw) {
				tipe = "income"
				break
			}
		}
		
		// Load planned keywords from database dynamically
		var keywords []string
		if DB != nil {
			rows, err := DB.Query("SELECT keyword FROM planned_keywords")
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var kw string
					if err := rows.Scan(&kw); err == nil {
						keywords = append(keywords, strings.ToLower(strings.TrimSpace(kw)))
					}
				}
			}
		}
		if len(keywords) == 0 {
			// Fallback defaults
			keywords = []string{"tagihan", "rencana", "planned", "nanti", "besok", "belum"}
		}

		// Detect status (paid by default, planned if contains "tagihan" or similar words/typos)
		status := "paid"
		
		// Clean and extract words for fuzzy matching
		wordRegex := regexp.MustCompile(`[^\w\s]`)
		cleanedText := wordRegex.ReplaceAllString(textLower, " ")
		words := strings.Fields(cleanedText)
		
		outerLoop:
		for _, kw := range keywords {
			// Substring check (matches compound phrases like "bayar nanti")
			if strings.Contains(textLower, kw) {
				status = "planned"
				break
			}
			
			// Typo matching word-by-word
			for _, w := range words {
				if len(w) >= 4 {
					dist := levenshteinDistance(w, kw)
					maxAllowed := 1
					if len(w) >= 7 {
						maxAllowed = 2
					}
					if dist <= maxAllowed {
						status = "planned"
						break outerLoop
					}
				}
			}
		}
		
		// Infer category
		kategori := InferCategory(textLower, tipe)
		
		// Clean description: strip nominals and common noise words
		deskripsi := messageText
		
		// Regex to remove numbers with suffixes
		suffixRx := regexp.MustCompile(`(?i)(\d+(?:[\.,]\d+)?)\s*(juta|jt|ribu|rb|k)`)
		deskripsi = suffixRx.ReplaceAllString(deskripsi, "")
		
		// Remove orphan numbers
		orphanRx := regexp.MustCompile(`\b\d+\b`)
		deskripsi = orphanRx.ReplaceAllString(deskripsi, "")
		
		// Remove words: rp, beli, bayar, catat, pemasukan, pengeluaran
		noiseRx := regexp.MustCompile(`(?i)(rp|beli|bayar|catat|pemasukan|pengeluaran|tagihan)`)
		deskripsi = noiseRx.ReplaceAllString(deskripsi, "")
		
		// Strip excessive whitespace
		deskripsi = strings.Join(strings.Fields(deskripsi), " ")
		deskripsi = strings.TrimSpace(deskripsi)
		
		// Title case or capitalize first letter
		if len(deskripsi) > 0 {
			deskripsi = strings.ToUpper(deskripsi[0:1]) + deskripsi[1:]
		} else {
			if tipe == "income" {
				deskripsi = "Pemasukan " + kategori
			} else {
				deskripsi = "Pengeluaran " + kategori
			}
		}
		
		tanggal := time.Now().Format("2006-01-02")
		
		return ParseResult{
			Intent: "INPUT_TRANSAKSI",
			Data: map[string]interface{}{
				"tanggal":   tanggal,
				"deskripsi": deskripsi,
				"kategori":  kategori,
				"tipe":      tipe,
				"nominal":   nominal,
				"status":    status,
			},
		}
	}
	
	// 2. UPDATE STATUS (bayar/lunas)
	// Check for update action without specifying a nominal
	payKeywords := []string{"bayar", "lunas", "dibayar", "transfer bayar"}
	isPayIntent := false
	for _, kw := range payKeywords {
		if strings.Contains(textLower, kw) {
			isPayIntent = true
			break
		}
	}
	
	if isPayIntent {
		// Detect which category is mentioned
		matchedCategory := ""
		// Look for explicit category names
		allCats := []string{"makan", "listrik", "transport", "internet", "belanja", "kesehatan", "pendidikan", "tempat tinggal", "gaji", "bonus", "sampingan"}
		for _, cat := range allCats {
			if strings.Contains(textLower, cat) {
				matchedCategory = cat
				break
			}
		}
		
		// Fallback: infer category from description keywords
		if matchedCategory == "" {
			matchedCategory = InferCategory(textLower, "expense")
		}
		
		return ParseResult{
			Intent: "UPDATE_STATUS",
			Data: map[string]interface{}{
				"category": matchedCategory,
				"rawText":  messageText,
			},
		}
	}
	
	// 3. QUERY (tanya data)
	queries := []struct {
		keywords []string
		qType    string
	}{
		{[]string{"saldo", "uang saya", "sisa uang", "duit"}, "SALDO_INQUIRY"},
		{[]string{"tagihan", "belum bayar", "belum dibayar", "rencana"}, "TAGIHAN_INQUIRY"},
		{[]string{"pengeluaran hari ini", "hari ini", "hariini"}, "TODAY_EXPENSE"},
		{[]string{"bulan ini", "habis berapa", "pengeluaran bulan", "total bulan"}, "MONTH_EXPENSE"},
		{[]string{"laporan", "report", "rekap"}, "REPORT_INQUIRY"},
	}
	
	for _, q := range queries {
		for _, kw := range q.keywords {
			if strings.Contains(textLower, kw) {
				return ParseResult{
					Intent: "QUERY",
					Data: map[string]interface{}{
						"queryType": q.qType,
						"rawText":   messageText,
					},
				}
			}
		}
	}
	
	// 4. UNKNOWN
	return ParseResult{
		Intent: "UNKNOWN",
		Data: map[string]interface{}{
			"rawText": messageText,
		},
	}
}

// Levenshtein distance helper for fuzzy matching
func levenshteinDistance(s, t string) int {
	sRunes := []rune(s)
	tRunes := []rune(t)
	sLen := len(sRunes)
	tLen := len(tRunes)

	d := make([][]int, sLen+1)
	for i := range d {
		d[i] = make([]int, tLen+1)
	}

	for i := 0; i <= sLen; i++ {
		d[i][0] = i
	}
	for j := 0; j <= tLen; j++ {
		d[0][j] = j
	}

	for i := 1; i <= sLen; i++ {
		for j := 1; j <= tLen; j++ {
			cost := 1
			if sRunes[i-1] == tRunes[j-1] {
				cost = 0
			}
			d[i][j] = minOfThree(
				d[i-1][j]+1,      // deletion
				d[i][j-1]+1,      // insertion
				d[i-1][j-1]+cost, // substitution
			)
		}
	}
	return d[sLen][tLen]
}

func minOfThree(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
