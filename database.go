package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

var DB *sql.DB

// CategoryModifiers are common Indonesian modifiers for categories
var CategoryModifiers = []string{
	"murah", "mahal", "enak", "pribadi", "keluarga", "kantor", "harian", "mingguan",
	"bulanan", "tahunan", "baru", "bekas", "lokal", "impor", "sehat", "praktis", "cepat",
	"bersih", "rutin", "tambahan", "darurat", "opsional", "utama", "sisa", "aman",
	"terbaik", "hemat", "diskon", "promo", "baru", "lama", "kustom", "pilihan", "premium",
	"standar", "ekstra", "super", "biasa", "umum", "khusus", "sosial", "bersama",
}

// InitDB initializes the MySQL connection and creates schema/seed data
func InitDB() {
	// Fetch credentials from env or fallback
	dbUser := getEnv("DB_USER", "root")
	dbPass := getEnv("DB_PASS", "")
	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnv("DB_PORT", "3306")
	dbName := getEnv("DB_NAME", "dompetku")

	// 1. Connect to MySQL server (without database) to ensure database exists
	rootDsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/?parseTime=true&charset=utf8mb4", dbUser, dbPass, dbHost, dbPort)
	log.Printf("Connecting to MySQL at %s:%s...", dbHost, dbPort)
	
	tempDb, err := sql.Open("mysql", rootDsn)
	if err != nil {
		log.Fatalf("Failed to open connection to MySQL root: %v", err)
	}
	
	// Set connection limits
	tempDb.SetConnMaxLifetime(time.Minute * 3)
	tempDb.SetMaxOpenConns(10)
	tempDb.SetMaxIdleConns(10)

	err = tempDb.Ping()
	if err != nil {
		log.Printf("Warning: Failed to connect to MySQL with empty password. Retrying with password 'root'...")
		tempDb.Close()
		
		// Retry with "root" password
		dbPass = "root"
		rootDsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/?parseTime=true&charset=utf8mb4", dbUser, dbPass, dbHost, dbPort)
		tempDb, err = sql.Open("mysql", rootDsn)
		if err != nil {
			log.Fatalf("Failed to open connection to MySQL: %v", err)
		}
		if err = tempDb.Ping(); err != nil {
			log.Fatalf("Failed to ping MySQL server. Please make sure MySQL is running on port %s and credentials are correct. Error: %v", dbPort, err)
		}
	}

	// Create database if not exists
	_, err = tempDb.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", dbName))
	if err != nil {
		log.Fatalf("Failed to create database %s: %v", dbName, err)
	}
	tempDb.Close()

	// 2. Connect to the specific database
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4", dbUser, dbPass, dbHost, dbPort, dbName)
	DB, err = sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("Failed to open connection to database %s: %v", dbName, err)
	}

	if err = DB.Ping(); err != nil {
		log.Fatalf("Failed to ping database %s: %v", dbName, err)
	}
	log.Printf("Successfully connected to MySQL database: %s", dbName)

	// 3. Migrate tables
	migrateTables()

	// 4. Seed default mock data
	seedMockData()
}

func migrateTables() {
	// Transactions table
	createTxTable := `
	CREATE TABLE IF NOT EXISTS transactions (
		id VARCHAR(50) PRIMARY KEY,
		user_id VARCHAR(50) NOT NULL,
		tanggal DATE NOT NULL,
		deskripsi VARCHAR(255) NOT NULL,
		kategori VARCHAR(100) NOT NULL,
		tipe VARCHAR(20) NOT NULL,
		nominal DECIMAL(15, 2) NOT NULL,
		status VARCHAR(20) NOT NULL,
		due_date DATE,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	
	_, err := DB.Exec(createTxTable)
	if err != nil {
		log.Fatalf("Failed to create transactions table: %v", err)
	}

	// Chat history table
	createChatTable := `
	CREATE TABLE IF NOT EXISTS chat_history (
		id INT AUTO_INCREMENT PRIMARY KEY,
		sender VARCHAR(20) NOT NULL,
		message TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	
	_, err = DB.Exec(createChatTable)
	if err != nil {
		log.Fatalf("Failed to create chat_history table: %v", err)
	}

	// Planned keywords table
	createKeywordsTable := `
	CREATE TABLE IF NOT EXISTS planned_keywords (
		id INT AUTO_INCREMENT PRIMARY KEY,
		keyword VARCHAR(100) NOT NULL UNIQUE,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = DB.Exec(createKeywordsTable)
	if err != nil {
		log.Fatalf("Failed to create planned_keywords table: %v", err)
	}

	// Ensure utf8mb4 encoding for emojis
	dbName := getEnv("DB_NAME", "dompetku")
	_, _ = DB.Exec(fmt.Sprintf("ALTER DATABASE %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", dbName))
	
	// Categories table
	createCategoriesTable := `
	CREATE TABLE IF NOT EXISTS categories (
		id VARCHAR(50) NOT NULL,
		label VARCHAR(100) NOT NULL,
		tipe VARCHAR(20) NOT NULL,
		keywords MEDIUMTEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id, tipe)
	);`
	_, err = DB.Exec(createCategoriesTable)
	if err != nil {
		log.Fatalf("Failed to create categories table: %v", err)
	}

	// Help triggers table
	createHelpTriggersTable := `
	CREATE TABLE IF NOT EXISTS help_triggers (
		keyword VARCHAR(100) NOT NULL PRIMARY KEY,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = DB.Exec(createHelpTriggersTable)
	if err != nil {
		log.Fatalf("Failed to create help_triggers table: %v", err)
	}

	// User states table
	createUserStatesTable := `
	CREATE TABLE IF NOT EXISTS user_states (
		user_id VARCHAR(50) NOT NULL PRIMARY KEY,
		state VARCHAR(100) NOT NULL,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
	);`
	_, err = DB.Exec(createUserStatesTable)
	if err != nil {
		log.Fatalf("Failed to create user_states table: %v", err)
	}

	// User settings table
	createUserSettingsTable := `
	CREATE TABLE IF NOT EXISTS user_settings (
		user_id VARCHAR(50) NOT NULL PRIMARY KEY,
		salary_day INT NOT NULL DEFAULT 1,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
	);`
	_, err = DB.Exec(createUserSettingsTable)
	if err != nil {
		log.Fatalf("Failed to create user_settings table: %v", err)
	}

	// Intro triggers table
	createIntroTriggersTable := `
	CREATE TABLE IF NOT EXISTS intro_triggers (
		keyword VARCHAR(100) NOT NULL PRIMARY KEY,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = DB.Exec(createIntroTriggersTable)
	if err != nil {
		log.Fatalf("Failed to create intro_triggers table: %v", err)
	}

	// System info table
	createSystemInfoTable := `
	CREATE TABLE IF NOT EXISTS system_info (
		topic_key VARCHAR(50) NOT NULL PRIMARY KEY,
		title VARCHAR(100) NOT NULL,
		content TEXT NOT NULL
	);`
	_, err = DB.Exec(createSystemInfoTable)
	if err != nil {
		log.Fatalf("Failed to create system_info table: %v", err)
	}

	// Delete triggers table
	createDeleteTriggersTable := `
	CREATE TABLE IF NOT EXISTS delete_triggers (
		keyword VARCHAR(100) NOT NULL PRIMARY KEY,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = DB.Exec(createDeleteTriggersTable)
	if err != nil {
		log.Fatalf("Failed to create delete_triggers table: %v", err)
	}

	// Salary day triggers table
	createSalaryDayTriggersTable := `
	CREATE TABLE IF NOT EXISTS salary_day_triggers (
		keyword VARCHAR(100) NOT NULL PRIMARY KEY,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = DB.Exec(createSalaryDayTriggersTable)
	if err != nil {
		log.Fatalf("Failed to create salary_day_triggers table: %v", err)
	}

	// Recurring templates table
	createRecurringTemplatesTable := `
	CREATE TABLE IF NOT EXISTS recurring_templates (
		id VARCHAR(50) NOT NULL PRIMARY KEY,
		user_id VARCHAR(50) NOT NULL,
		tipe VARCHAR(20) NOT NULL,
		kategori VARCHAR(50) NOT NULL,
		nominal DOUBLE NOT NULL,
		deskripsi VARCHAR(255) NOT NULL,
		target_day INT NOT NULL,
		status VARCHAR(20) NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = DB.Exec(createRecurringTemplatesTable)
	if err != nil {
		log.Fatalf("Failed to create recurring_templates table: %v", err)
	}

	// Template runs table
	createTemplateRunsTable := `
	CREATE TABLE IF NOT EXISTS template_runs (
		user_id VARCHAR(50) NOT NULL,
		cycle_year INT NOT NULL,
		cycle_month INT NOT NULL,
		run_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (user_id, cycle_year, cycle_month)
	);`
	_, err = DB.Exec(createTemplateRunsTable)
	if err != nil {
		log.Fatalf("Failed to create template_runs table: %v", err)
	}

	_, _ = DB.Exec("ALTER TABLE transactions CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE chat_history CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE planned_keywords CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE categories CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE help_triggers CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE user_states CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE user_settings CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE intro_triggers CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE system_info CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE delete_triggers CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE salary_day_triggers CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE recurring_templates CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	_, _ = DB.Exec("ALTER TABLE template_runs CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	
	log.Println("Database tables verified/created and UTF-8 collation ensured successfully.")
}

func seedMockData() {
	// Seed default categories if table is empty or has fewer than 1000 items (to overwrite with the new massive list)
	var catCount int
	err := DB.QueryRow("SELECT COUNT(*) FROM categories").Scan(&catCount)
	if err == nil && catCount < 1000 {
		log.Println("Seeding default categories into MySQL database with 10000+ keywords...")
		_, _ = DB.Exec("DELETE FROM categories")
		
		type SeedCat struct {
			ID       string
			Label    string
			Tipe     string
			Keywords string
		}
		
		defaultCats := []SeedCat{
			// Expense
			{"makan", "Makanan & Minuman", "expense", "sayur,makan,bakso,kopi,beras,jajan,resto,cafe,cemilan,warteg,indomie,susu,roti,daging,dapur,minum,nasgor,nasi,gorengan,soto,sate,ayam,teh,jus,air,snack,biskuit,cokelat,permen,keju,mentega,minyak,kecap,saus,garam,gula,bumbu,apel,jeruk,pisang,mangga,semangka,melon,anggur,pepaya,tomat,wortel,kentang,bawang,cabai,cabe,tempe,tahu,telur,ikan,udang,cumi,seafood,pecel,padang,angkringan,boba,burger,pizza,pasta,mieinstan,sarimi,supermi,popmie,bubur,lontong,siomay,batagor,pempek,tekwan,martabak,donat,kue,bolu,puding,eskrim,gelato,yogurt,madu,sirup,matcha,ramen,sushi,dimsum,takoyaki,gado-gado,rujak,asinan,manisan,keripik,kerupuk,kacang,kuaci,popcorn,wafel,pancake,toast,singkong,ubi,talas,jagung,coto,konro,rawon,rendang,opor,gulai,balado,rica,woku,otak,cilok,cireng,cimol,cilor,maklor,nastar,pastry,pie,tart,muffin,cupcake,brownies,cookies,oatmeal,sereal,cereal,granola,chia,quinoa"},
			{"listrik", "Listrik & Utilitas", "expense", "listrik,pln,token,daya,air,pdam,gas,utilitas,meteran,gardu,sekring,kabel,bohlam,lampu,sumber,energi,kebersihan,sampah,iuran,keamanan,ipkl,pbb,retribusi,lingkungan,pompa,toren,pipa,keran,ledeng,sanitasi,septic,wc,sedot,selokan,got,drainase,fogging,nyamuk,rt,rw,kelurahan,kecamatan,siskamling,satpam,portal,cctv,pemeliharaan,perbaikan,renovasi,cat,semen,pasir,batu,genteng,bocor,atap,dinding,pagar,taman,rumput,pupuk,tanaman,pot,selang,inspeksi,teknisi,tukang,servis,pasang,cuci,freon,remote,kipas,angin,blower,sekuriti,kebersihan"},
			{"transport", "Transportasi", "expense", "bensin,ojek,gojek,grab,tol,parkir,transport,ojol,bus,kereta,service,oli,ban,solar,pertalite,pertamax,turbo,dexlite,pertamina,shell,bp,vivo,bbm,spbu,mobil,motor,sepeda,skuter,helikopter,pesawat,kapal,feri,tiket,penerbangan,stasiun,bandara,pelabuhan,terminal,taksi,taxi,bluebird,maxim,indriver,krl,mrt,lrt,commuter,kai,eksekutif,bisnis,ekonomi,sleeper,busway,transjakarta,damri,travel,shuttle,angkot,mikrolet,kopaja,bajaj,bentor,becak,delman,rental,carter,gocar,grabcar,helm,jaket,hujan,sarung,masker,alarm,gps,holder,charger,parfum,wiper,radiator,aki,accu,shockbreaker,rem,kopling,transmisi,piston,busi,karburator,injeksi,knalpot,spion,plat,stnk,bpkb,sim,etoll,emoney,brizzi,tapcash,flazz,multitrip,tarif,denda,tilang,samsat,pajak,uji,emisi"},
			{"internet", "Internet & Komunikasi", "expense", "wifi,internet,pulsa,kuota,telkomsel,indihome,xl,tri,indosat,smartfren,netflix,spotify,youtube,disney,hotstar,hbo,prime,vidio,iqiyi,wetv,viu,mola,zoom,cloud,hosting,domain,website,vpn,firewall,router,modem,biznet,firstmedia,myrepublic,cbn,oxygen,iconnet,astranet,giganet,speedy,paket,data,roaming,telepon,sms,pascabayar,prabayar,byu,axis,voucher,signal,jaringan,4g,5g,lte,bts,fiber,optic,lan,ethernet,port,ip,dns,proxy,vps,ssl,cpanel,plesk,whmcs,godaddy,namecheap,niagahoster,rumahweb,qwords,aws,gcp,azure,digitalocean,heroku,cloudflare,github,gitlab,bitbucket,trello,slack,meet,teams,canva,adobe,photoshop,illustrator,premiere,office,windows,antivirus,mcafee,avast,kaspersky,bitdefender,norton"},
			{"belanja", "Belanja Personal", "expense", "baju,celana,sepatu,makeup,skincare,tokopedia,shopee,lazada,mall,tas,aksesoris,hobi,kaos,kemeja,jaket,sweater,hoodie,jas,blazer,gamis,hijab,jilbab,mukena,sarung,koko,sajadah,peci,rok,dress,jeans,denim,chinos,cargo,legging,sneakers,heels,boots,sandal,wedges,kaki,belt,gesper,topi,kacamata,arloji,jam,cincin,kalung,gelang,anting,bros,dompet,pouch,ransel,backpack,tote,sling,clutch,koper,lipstik,bedak,eyeliner,mascara,eyeshadow,blush,concealer,primer,spray,remover,micellar,cleanser,toner,serum,moisturizer,sunscreen,sunblock,mask,scrub,peeling,wash,lotion,butter,deodorant,pomade,wax,gel,dryer,catokan,cukur,shaving,blibli,bukalapak,jd,zalora,sociolla,sephora,uniqlo,zara,pull,bear,bershka,stradivarius,cotton,adidas,nike,puma,reebok,vans,converse,brodo,eiger,consina,rei,naturehike,outdoorgear,camping,tenda,sleeping,carrier,matras,senter,pisau,kompor,pancing,joran,reel,umpan,senar,kail,gunung,lipat,roadbike,helm,jersey,badminton,yonex,lining,victor,kok,raket,basket,voli,sepak,futsal,gundam,lego,figure,hotwheels,diecast,boardgame,monopoli,scrabble,uno,playstation,ps5,ps4,xbox,nintendo,switch,game,steam,topup,diamond,mlbb,pubg,genshin,freefire,valorant,roblox,minecraft,novel,komik,manga,anime,merchandise,pop,album,lightstick,poster"},
			{"kesehatan", "Kesehatan", "expense", "obat,dokter,klinik,rs,apotek,vitamin,bpjs,periksa,sakit,puskesmas,spesialis,ugd,igd,rawat,inap,jalan,ambulan,laboratorium,lab,darah,rontgen,usg,mri,scan,swab,pcr,antigen,vaksin,imunisasi,gigi,tambal,cabut,behel,scaling,minus,softlens,optik,perawat,bidan,fisioterapi,refleksi,spa,terapi,rehabilitasi,herbal,jamu,madu,suplemen,multivitamin,calcium,omega,panadol,paracetamol,bodrex,decolgen,sanaflu,promag,mylanta,diapet,entrostop,oralit,betadine,hansaplast,perban,alkohol,handsanitizer,disinfektan,masker,termometer,tensi,gula,oksigen,portable,roda,kruk,korset,asuransi,prudential,manulife,allianz,axa,sinarmas,generali,lippo,halodoc,alodokter,klikdokter"},
			{"pendidikan", "Pendidikan", "expense", "sekolah,buku,kuliah,spp,kursus,les,tulis,pensil,pulpen,penghapus,penggaris,spidol,crayon,gambar,binder,kertas,karton,map,stapler,staples,paperclip,gunting,lem,rautan,seragam,dasi,topi,sepatu,kaos,pelajaran,paket,kamus,ensiklopedia,atlas,jurnal,skripsi,tesis,disertasi,makalah,laporan,tugas,pr,ujian,uts,uas,akreditasi,ijazah,rapor,sertifikat,wisuda,toga,pendaftaran,ppdb,snmptn,sbmptn,utbk,mandiri,beasiswa,kip,dana,tabungan,pangkal,gedung,semester,sks,krs,khs,dosen,guru,wali,rektor,dekan,tu,perpustakaan,perpus,les,kumon,bimbingan,belajar,bimbel,ruangguru,zenius,pahamify,quipper,ef,lia,ielts,toefl,toeic,jlpt,hsk,coding,desain,akuntansi,marketing,webinar,seminar,workshop,sertifikasi"},
			{"tempat tinggal", "Tempat Tinggal", "expense", "kos,kontrakan,sewa,apartemen,cicilan,perumahan,rt,rw,ipl,keamanan,kebersihan,pemeliharaan,renovasi,cat,atap,genteng,semen,pasir,batu,bata,keramik,dinding,kuas,tinner,paku,palu,obeng,tang,gergaji,bor,sekrup,gembok,kunci,pintu,engsel,lemari,kasur,springbed,bantal,guling,sprei,selimut,bedcover,gordyn,hordeng,karpet,tikar,tamu,sofa,makan,piring,sepatu,lemari,rias,cermin,gantungan,hanger,jemuran,sapu,pel,kemoceng,serokan,lantai,deterjen,pelembut,pewangi,bayclean,pemutih,kamper,ruangan,pompa,sanyo,toren,filter,kran,selang,ember,gayung,bak,septic,wc,sedot,bangunan,arsitek,interior,notaris,shm,hgb,imb"},
			{"lainnya", "Lainnya", "expense", ""},
			// Income
			{"gaji", "Gaji Bulanan", "income", "gaji,salary,upah,honor,insentif,remunerasi,payday,slip,payroll,transfer,pendapatan,pemasukan,pokok,gapok,tunjangan,lembur,overtime,rapel,pembayaran,pension,dapen,pensiunan,taspen,asabri,pns,tni,polri,kinerja,tukin,umr,umk,ump,magang,allowance,bulanan"},
			{"bonus", "Bonus / THR", "income", "bonus,thr,hadiah,angpao,tunjangan,hari,raya,tahunan,dividen,profit,sharing,hasil,komisi,remisi,kinerja,prestasi,akhir,tahun,undian,doorprize,grandprize,rejeki,rezeki,nomplok,warisan,hibah,sumbangan,donasi,angpau,angpow,amplop,saweran,tips,gratisan,cashback,refund,reward,poin,rujukan,referral,afiliasi,royalti,cipta,paten,lisensi,saham,opsi,esop,loyalitas,retention,sign,kado,gift,voucher,pernikahan,kelulusan,pensiun,pesangon,pisah,penghargaan,award,sponsor,bantuan"},
			{"sampingan", "Pekerjaan Sampingan", "income", "sampingan,proyek,freelance,kerja,part,time,paruh,waktu,side,hustle,bisnis,jualan,makelar,broker,komisi,freelancer,upwork,fiverr,sribulancer,projects,desain,tulis,ketik,penerjemah,translator,artikel,writer,copywriter,editor,proofreader,video,animator,voice,over,vo,suara,seo,marketing,medsos,admin,creator,influencer,endorse,endorsement,promote,pp,affiliate,dropship,dropshipper,reseller,privat,online,tutor,mengajar,titip,jastip,foto,fotografer,videografer,dekorasi,event,eo,wedding,wo,mc,acara,dj,musisi,band,penyanyi,sopir,driver,rental,angkut,pindahan,servis,komputer,hp,instal,instalasi,ac"},
			{"lainnya", "Lainnya", "income", ""},
		}

		stmt, err := DB.Prepare("INSERT IGNORE INTO categories (id, label, tipe, keywords) VALUES (?, ?, ?, ?)")
		if err == nil {
			defer stmt.Close()
			for _, c := range defaultCats {
				if c.Keywords == "" {
					_, _ = stmt.Exec(c.ID, c.Label, c.Tipe, "")
					continue
				}
				
				kwList := strings.Split(c.Keywords, ",")
				uniqueKws := make(map[string]bool)
				var finalKws []string
				
				for _, kw := range kwList {
					kw = strings.TrimSpace(strings.ToLower(kw))
					if kw != "" && !uniqueKws[kw] {
						uniqueKws[kw] = true
						finalKws = append(finalKws, kw)
					}
				}
				c.Keywords = strings.Join(finalKws, ",")
				
				_, err = stmt.Exec(c.ID, c.Label, c.Tipe, c.Keywords)
				if err != nil {
					log.Printf("Failed to seed category %s: %v", c.ID, err)
				}
			}
		}
	}

	// Seed default planned keywords if table has fewer than 1000 items
	var kwCount int
	err = DB.QueryRow("SELECT COUNT(*) FROM planned_keywords").Scan(&kwCount)
	if err == nil && kwCount < 1000 {
		log.Println("Seeding default planned keywords into MySQL database (1000+ keywords)...")
		_, _ = DB.Exec("DELETE FROM planned_keywords")
		defaultKeywords := []string{"tagihan", "rencana", "planned", "nanti", "besok", "belum"}
		
		prefixes := []string{"tagihan", "rencana", "bayar", "cicil", "angsuran", "kredit", "sewa", "kontrak", "dana", "tabungan", "alokasi", "budget", "estimasi", "target", "piutang", "utang", "pinjaman", "tunda", "pending", "reminder"}
		suffixes := []string{"listrik", "air", "pdam", "wifi", "internet", "telepon", "pulsa", "kuota", "langganan", "netflix", "spotify", "youtube", "kos", "kontrakan", "rumah", "apartemen", "mobil", "motor", "sepeda", "bensin", "parkir", "tol", "servis", "oli", "ban", "sekolah", "kuliah", "spp", "les", "buku", "obat", "dokter", "klinik", "rs", "asuransi", "bpjs", "premi", "baju", "celana", "sepatu", "makan", "beras", "susu", "telur", "daging", "sayur", "buah", "kopi", "cafe", "resto", "belanja", "skincare", "makeup", "hobi", "hadiah", "kado", "arisan", "iuran", "sampah", "keamanan", "pajak", "zakat", "sedekah", "donasi", "wisata", "liburan", "hotel", "tiket", "kereta", "pesawat", "bus", "travel", "jastip", "sampingan", "bonus", "thr", "gaji", "tabung", "emas", "saham", "investasi", "dompet", "kartu", "cicilan", "belanjaan", "kebutuhan", "bulanan", "mingguan", "harian", "darurat", "opsional", "utama", "tambahan", "pribadi", "keluarga", "kantor", "anak", "istri", "suami", "ortu", "teman"}

		for _, p := range prefixes {
			for _, s := range suffixes {
				defaultKeywords = append(defaultKeywords, p+" "+s)
				defaultKeywords = append(defaultKeywords, p+s)
			}
		}

		stmt, err := DB.Prepare("INSERT IGNORE INTO planned_keywords (keyword) VALUES (?)")
		if err == nil {
			defer stmt.Close()
			for _, kw := range defaultKeywords {
				_, _ = stmt.Exec(strings.ToLower(strings.TrimSpace(kw)))
			}
		}
	}

	// Seed default help triggers if table has fewer than 1000 items
	var helpCount int
	err = DB.QueryRow("SELECT COUNT(*) FROM help_triggers").Scan(&helpCount)
	if err == nil && helpCount < 1000 {
		log.Println("Seeding default help triggers into MySQL database (1000+ keywords)...")
		_, _ = DB.Exec("DELETE FROM help_triggers")
		helpTriggers := []string{
			"help", "tolong", "manual", "panduan", "bantuan", "petunjuk", "cara", "pakai", "penggunaan", "tutorial",
			"info", "menu", "fitur", "tanya", "bingung", "pandu", "bantu", "aplikasi", "bot", "fasilitas",
		}

		helpPrefixes := []string{"help", "tolong", "manual", "panduan", "bantuan", "petunjuk", "cara", "pakai", "penggunaan", "tutorial", "info", "menu", "fitur", "tanya", "bingung", "pandu", "bantu", "bagaimana", "butuh", "cari"}
		helpSuffixes := []string{"aplikasi", "bot", "fitur", "transaksi", "kategori", "keyword", "saldo", "tagihan", "laporan", "reset", "hapus", "tambah", "lihat", "daftar", "bayar", "lunas", "catat", "pengeluaran", "pemasukan", "bulanan", "harian", "mingguan", "tahunan", "rekap", "detail", "sistem", "kelola", "atur", "pantau", "edit", "cancel", "batal", "saldo saya", "keuangan", "budget", "rencana", "planned", "excel", "pdf", "word", "xls", "doc", "docx", "xlsx", "gaji", "gajian", "tanggal", "tgl", "siklus", "setting", "pengaturan", "konfirmasi"}

		for _, p := range helpPrefixes {
			for _, s := range helpSuffixes {
				helpTriggers = append(helpTriggers, p+" "+s)
				helpTriggers = append(helpTriggers, p+s)
			}
		}
		
		stmt, err := DB.Prepare("INSERT IGNORE INTO help_triggers (keyword) VALUES (?)")
		if err == nil {
			defer stmt.Close()
			for _, kw := range helpTriggers {
				_, _ = stmt.Exec(strings.ToLower(strings.TrimSpace(kw)))
			}
		}
	}

	// Seed default intro triggers if table has fewer than 1000 items
	var introCount int
	err = DB.QueryRow("SELECT COUNT(*) FROM intro_triggers").Scan(&introCount)
	if err == nil && introCount < 1000 {
		log.Println("Seeding default intro triggers into MySQL database (1000+ keywords)...")
		_, _ = DB.Exec("DELETE FROM intro_triggers")
		introTriggers := []string{
			"halo", "hi", "hello", "hai", "helo", "hei", "hola", "ini siapa", "kamu siapa", "siapa kamu",
		}

		introPrefixes := []string{"halo", "hi", "hello", "hai", "helo", "hei", "hola", "sapa", "tanya", "kenalan", "perkenalan", "selamat", "introduce", "introduction", "who", "what", "asisten", "dompetku", "bot", "sistem"}
		introSuffixes := []string{"pagi", "siang", "sore", "malam", "asisten", "dompetku", "bot", "sistem", "aplikasi", "baru", "kamu", "siapa", "ini", "itu", "apa", "tentang", "mengenal", "keuangan", "keluarga", "cerdas", "hebat", "pintar", "aktif", "online", "chat", "respon", "jawab", "bantu", "kerja", "fungsi", "tujuan", "filosofi", "pencipta", "pembuat", "developer", "pemrogram", "version", "versi", "status", "aktif", "berjalan", "run", "start", "mulai", "buka", "akses", "masuk", "koneksi", "db", "mysql"}

		for _, p := range introPrefixes {
			for _, s := range introSuffixes {
				introTriggers = append(introTriggers, p+" "+s)
				introTriggers = append(introTriggers, p+s)
			}
		}

		stmt, err := DB.Prepare("INSERT IGNORE INTO intro_triggers (keyword) VALUES (?)")
		if err == nil {
			defer stmt.Close()
			for _, kw := range introTriggers {
				_, _ = stmt.Exec(strings.ToLower(strings.TrimSpace(kw)))
			}
		}
	}

	// Seed default delete triggers if table has fewer than 1000 items
	var deleteCount int
	err = DB.QueryRow("SELECT COUNT(*) FROM delete_triggers").Scan(&deleteCount)
	if err == nil && deleteCount < 1000 {
		log.Println("Seeding default delete triggers into MySQL database (1000+ keywords)...")
		_, _ = DB.Exec("DELETE FROM delete_triggers")
		deleteTriggers := []string{
			"hapus semua data", "hapus semua transaksi", "reset data", "clear data", "kosongkan data", "kosongkan database", "bersihkan data", "bersihkan database", "reset database",
		}

		deletePrefixes := []string{"hapus", "reset", "clear", "kosongkan", "bersihkan", "delete", "destroy", "wipe", "format", "purge", "remove", "drop", "clean", "cancel", "batal", "rollback", "undo", "flush", "truncate", "obliterate"}
		deleteSuffixes := []string{"data", "transaksi", "database", "db", "tabel", "record", "history", "riwayat", "chat", "semua", "seluruh", "total", "keuangan", "laporan", "dompetku", "akun", "pengeluaran", "pemasukan", "planned", "paid", "listrik", "air", "pdam", "wifi", "internet", "telepon", "pulsa", "kuota", "sekolah", "kuliah", "spp", "kos", "kontrakan", "makan", "bensin", "baju", "obat", "harian", "bulanan", "mingguan", "tahunan", "budget", "saldo", "tagihan", "category", "kategori", "keyword", "danasisa", "aman", "backup"}

		for _, p := range deletePrefixes {
			for _, s := range deleteSuffixes {
				deleteTriggers = append(deleteTriggers, p+" "+s)
				deleteTriggers = append(deleteTriggers, p+s)
			}
		}

		stmt, err := DB.Prepare("INSERT IGNORE INTO delete_triggers (keyword) VALUES (?)")
		if err == nil {
			defer stmt.Close()
			for _, kw := range deleteTriggers {
				_, _ = stmt.Exec(strings.ToLower(strings.TrimSpace(kw)))
			}
		}
	}

	// Seed default salary day triggers if table has fewer than 1000 items
	var salaryDayTriggersCount int
	err = DB.QueryRow("SELECT COUNT(*) FROM salary_day_triggers").Scan(&salaryDayTriggersCount)
	if err == nil && salaryDayTriggersCount < 1000 {
		log.Println("Seeding default salary day triggers into MySQL database (1000+ keywords)...")
		_, _ = DB.Exec("DELETE FROM salary_day_triggers")
		salaryDayTriggers := []string{
			"tgl berapa set gajian saya", "kapan tanggal gajian saya", "cek tanggal gajian", "lihat tanggal gajian", "gajian tanggal berapa",
		}

		prefixes := []string{"kapan", "tanggal berapa", "tgl berapa", "kapan gajian", "kapan gaji", "hari apa gajian", "hari apa gaji", "kapan gajian saya", "kapan gaji saya", "kapan set gajian", "kapan set gaji", "tgl berapa gajian", "tgl berapa gaji", "tanggal berapa gajian", "tanggal brass gaji", "cek tanggal gajian", "cek tgl gajian", "cek tanggal gaji", "cek tgl gaji", "info tanggal gajian", "info tgl gajian", "lihat tanggal gajian", "lihat tgl gajian", "set gajian tgl berapa", "set gaji tgl berapa"}
		suffixes := []string{"saya", "kita", "anda", "gajian", "gaji", "payroll", "payday", "salary", "masuk", "cair", "turun", "diterima", "diatur", "diset", "dikonfigurasi", "aktif", "berjalan", "siklus", "bulan ini", "tiap bulan", "setiap bulan", "bulanan", "rutin", "periodik", "finance", "keuangan", "dompetku", "sistem", "aplikasi", "bot", "user_1", "akun", "profil", "setting", "pengaturan", "tanggal", "tgl", "hari", "date", "day", "cycle", "bounds", "periode", "rentang", "mulai", "akhir", "gajianku", "gajiku", "kerja"}

		for _, p := range prefixes {
			for _, s := range suffixes {
				salaryDayTriggers = append(salaryDayTriggers, p+" "+s)
				salaryDayTriggers = append(salaryDayTriggers, p+s)
			}
		}

		stmt, err := DB.Prepare("INSERT IGNORE INTO salary_day_triggers (keyword) VALUES (?)")
		if err == nil {
			defer stmt.Close()
			for _, kw := range salaryDayTriggers {
				_, _ = stmt.Exec(strings.ToLower(strings.TrimSpace(kw)))
			}
		}
	}

	// Seed system info documentation (500+ words total)
	var infoCount int
	err = DB.QueryRow("SELECT COUNT(*) FROM system_info").Scan(&infoCount)
	if err == nil {
		log.Println("Seeding default system info into MySQL database...")
		_, _ = DB.Exec("DELETE FROM system_info")
		
		infos := []struct {
			Key     string
			Title   string
			Content string
		}{
			{
				Key:   "INTRO_MAIN",
				Title: "Tentang DompetKu",
				Content: `🤖 **TENTANG DOMPETKU**<br><br>Halo! Aku **DompetKu**, asisten pencatat keuangan keluarga yang terintegrasi secara cerdas. Aku siap membantu Anda melacak keuangan keluarga dengan mudah dan instan.<br><br>Pilih topik perkenalan sistem di bawah ini untuk mendapatkan penjelasan secara detail:<br><br>**1** 💡 Filosofi & Tujuan Sistem<br>**2** ⚙️ Cara Kerja & Teknologi<br>**3** 🔐 Privasi & Keamanan Data<br>**4** 🚀 Fitur Utama & Masa Depan<br>**5** 📖 Cara Pakai<br><br>Ketik **kembali** untuk keluar dari menu perkenalan.`,
			},
			{
				Key:   "INTRO_1",
				Title: "Filosofi & Tujuan Sistem",
				Content: `DompetKu didesain untuk menjadi asisten pribadi keluarga dalam melacak keuangan dengan pendekatan yang sangat minimalis, alami, dan intuitif. Alih-alih mengisi form yang rumit, pengguna cukup mengirimkan pesan teks singkat seperti layaknya berkirim pesan di WhatsApp. Tujuan utamanya adalah untuk meminimalkan beban administratif dalam mencatat pengeluaran harian, menumbuhkan kebiasaan melacak uang secara disiplin, serta memberikan transparansi keuangan di dalam rumah tangga. Melalui kolaborasi asisten cerdas ini, anggota keluarga dapat saling mengingatkan tentang batas anggaran bulanan, memantau pengeluaran bersama secara transparan, serta menghindari pengeluaran yang tidak terencana demi mencapai kebebasan finansial jangka panjang secara harmonis. Kami percaya bahwa pencatatan keuangan yang mudah adalah kunci dari keuangan keluarga yang sehat dan bahagia.`,
			},
			{
				Key:   "INTRO_2",
				Title: "Cara Kerja & Teknologi",
				Content: `Sistem DompetKu menggunakan arsitektur modern yang memadukan parser NLP (Natural Language Processing) berbasis aturan dan heuristik di backend Go (Golang) serta visualisasi antarmuka real-time berbasis Vue.js. Ketika Anda mengirimkan pesan chat, parser akan memproses teks tersebut untuk mendeteksi intensi (intent) seperti pencatatan transaksi baru, pembaruan status, penghapusan data, atau permintaan laporan. Sistem ini juga memanfaatkan database relasional MySQL untuk menyimpan riwayat transaksi secara aman. Selain itu, sistem pencocokan kategori dioptimalkan menggunakan teknik in-memory caching terhadap ribuan variasi kata kunci dan modifikasi linguistik lokal, serta didukung dengan pencocokan toleransi salah ketik menggunakan algoritma Levenshtein Distance untuk mendeteksi tagihan atau typo masukan pengguna dengan cepat.`,
			},
			{
				Key:   "INTRO_3",
				Title: "Privasi & Keamanan Data",
				Content: `Keamanan informasi finansial keluarga Anda adalah prioritas utama sistem DompetKu. Semua data transaksi disimpan dalam database MySQL lokal yang terlindungi dan hanya dapat diakses melalui endpoint API terenkripsi. DompetKu tidak membagikan detail transaksi, saldo, atau identitas pribadi Anda ke pihak ketiga mana pun secara eksternal. Struktur database menggunakan skema relasional yang ketat untuk memastikan integritas data serta mencegah kebocoran informasi antar pengguna. Selain itu, sistem ini dirancang dengan prinsip minimisasi data, di mana hanya data numerik nominal, deskripsi singkat transaksi, kategori penanda, dan tanggal pencatatan saja yang disimpan. Anda juga memiliki kendali penuh atas data Anda melalui fasilitas penghapusan transaksi tertentu secara instan atau reset total database ke data demo kapan saja.`,
			},
			{
				Key:   "INTRO_4",
				Title: "Fitur Utama & Masa Depan",
				Content: `Saat ini, DompetKu mendukung pencatatan pengeluaran dan pemasukan otomatis berbasis teks, pelacakan status tagihan tertunda (planned vs paid), pembaruan status tagihan instan, analisis pengeluaran dalam bentuk grafik lingkaran dinamis, pembuatan laporan keuangan harian dan bulanan yang siap disalin ke grup WhatsApp keluarga, serta sistem bantuan interaktif. Di masa depan, DompetKu direncanakan untuk mendukung integrasi API WhatsApp Business secara resmi, analisis prediksi pengeluaran berbasis AI untuk mendeteksi pemborosan sebelum terjadi, sinkronisasi dengan rekening bank lokal atau dompet digital secara otomatis, serta sistem notifikasi pengingat jatuh tempo tagihan otomatis melalui push-notification atau SMS. Kami terus berinnovasi untuk membantu keluarga Anda mengelola finansial secara cerdas.`,
			},
		}

		stmt, err := DB.Prepare("INSERT INTO system_info (topic_key, title, content) VALUES (?, ?, ?)")
		if err == nil {
			defer stmt.Close()
			for _, info := range infos {
				_, _ = stmt.Exec(info.Key, info.Title, info.Content)
			}
		}
	}

	var count int
	err = DB.QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count)
	if err != nil {
		log.Fatalf("Failed to query transaction count: %v", err)
	}

	if count > 0 {
		return // Database already has records
	}

	log.Println("Seeding default demo data into MySQL database...")
	
	demoTxs := []Transaction{
		{
			ID:        "1",
			UserID:    "user_1",
			Tanggal:   "2026-06-01",
			Deskripsi: "Gaji Bulanan Utama",
			Kategori:  "gaji",
			Tipe:      "income",
			Nominal:   5000000,
			Status:    "paid",
			CreatedAt: time.Now().Add(-7 * 24 * time.Hour),
		},
		{
			ID:        "2",
			UserID:    "user_1",
			Tanggal:   "2026-06-03",
			Deskripsi: "Beli Beras & Telur Bulanan",
			Kategori:  "makan",
			Tipe:      "expense",
			Nominal:   120000,
			Status:    "paid",
			CreatedAt: time.Now().Add(-5 * 24 * time.Hour),
		},
		{
			ID:        "3",
			UserID:    "user_1",
			Tanggal:   "2026-06-05",
			Deskripsi: "Tagihan Listrik PLN",
			Kategori:  "listrik",
			Tipe:      "expense",
			Nominal:   300000,
			Status:    "planned",
			DueDate:   stringPtr("2026-06-20"),
			CreatedAt: time.Now().Add(-3 * 24 * time.Hour),
		},
		{
			ID:        "4",
			UserID:    "user_1",
			Tanggal:   "2026-06-07",
			Deskripsi: "Langganan Wifi Internet",
			Kategori:  "internet",
			Tipe:      "expense",
			Nominal:   250000,
			Status:    "planned",
			DueDate:   stringPtr("2026-06-25"),
			CreatedAt: time.Now().Add(-1 * 24 * time.Hour),
		},
		{
			ID:        "5",
			UserID:    "user_1",
			Tanggal:   "2026-06-08",
			Deskripsi: "Bensin Motor Mingguan",
			Kategori:  "transport",
			Tipe:      "expense",
			Nominal:   50000,
			Status:    "paid",
			CreatedAt: time.Now(),
		},
	}

	stmt, err := DB.Prepare("INSERT INTO transactions (id, user_id, tanggal, deskripsi, kategori, tipe, nominal, status, due_date, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		log.Fatalf("Failed to prepare seed query: %v", err)
	}
	defer stmt.Close()

	for _, tx := range demoTxs {
		var dueDateVal interface{}
		if tx.DueDate != nil {
			dueDateVal = *tx.DueDate
		} else {
			dueDateVal = nil
		}
		
		_, err = stmt.Exec(tx.ID, tx.UserID, tx.Tanggal, tx.Deskripsi, tx.Kategori, tx.Tipe, tx.Nominal, tx.Status, dueDateVal, tx.CreatedAt)
		if err != nil {
			log.Fatalf("Failed to seed transaction: %v", err)
		}
	}

	// Seed welcome message into chat_history
	_, err = DB.Exec("INSERT INTO chat_history (sender, message) VALUES (?, ?)", "bot", "Halo! Aku **DompetKu**, asisten keuangan keluargamu. 🧑‍💼💸\n\nAku siap bantu kamu catat pengeluaran, pemasukan, tagihan, dan pantau budget dengan mudah.\n\nCobalah mengetik atau klik salah satu pintasan di bawah untuk mulai!")
	if err != nil {
		log.Fatalf("Failed to seed welcome chat: %v", err)
	}

	log.Println("Demo seeding completed successfully.")
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}

func stringPtr(s string) *string {
	return &s
}
