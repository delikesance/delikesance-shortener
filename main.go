package main

import (
	"html/template"
	"log"
	"net/http"
	"sync"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var templatePool = sync.Pool{
	New: func() interface{} {
		return template.Must(template.ParseGlob("templates/*.html"))
	},
}

type Link struct {
	ID          string `gorm:"primaryKey;index:idx_id,length:10"`
	URL         string `gorm:"index:idx_url,length:512"`
	ClickCount  int    `gorm:"index:idx_click_count"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ClickEvents []ClickEvent `gorm:"foreignKey:LinkID;constraint:OnDelete:CASCADE"`
}

type ClickEvent struct {
	ID        uint      `gorm:"primaryKey"`
	LinkID    string    `gorm:"index:idx_link_id,length:10"`
	Referer   string    `gorm:"index:idx_referer,length:512"`
	Timestamp time.Time `gorm:"index:idx_timestamp"`
}

func incrementString(s string) string {
	chars := []byte(s)
	for i := len(chars) - 1; i >= 0; i-- {
		switch {
		case chars[i] == '9':
			chars[i] = 'a'
			return string(chars)
		case chars[i] == 'z':
			chars[i] = 'A'
			return string(chars)
		case chars[i] == 'Z':
			chars[i] = '0'
		case (chars[i] >= '0' && chars[i] < '9') ||
			(chars[i] >= 'a' && chars[i] < 'z') ||
			(chars[i] >= 'A' && chars[i] < 'Z'):
			chars[i]++
			return string(chars)
		}
	}
	return "0" + string(chars)
}

func createLink(url string, db *gorm.DB, mu *sync.Mutex) *Link {
	mu.Lock()
	defer mu.Unlock()

	var lastLink Link
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Order("id DESC").
		First(&lastLink).Error; err != nil && err != gorm.ErrRecordNotFound {
		log.Printf("Error fetching last link: %v", err)
		return nil
	}

	newID := "0000"
	if lastLink.ID != "" {
		newID = incrementString(lastLink.ID)
	}

	newLink := &Link{
		ID:  newID,
		URL: url,
	}

	if err := db.Create(newLink).Error; err != nil {
		log.Printf("Error creating new link: %v", err)
		return nil
	}

	return newLink
}

func main() {
	db := setupDatabase()
	setupServer(db)
}

func setupDatabase() *gorm.DB {
	db, err := gorm.Open(sqlite.Open("file:gorm.db?cache=shared&_journal_mode=WAL"), &gorm.Config{
		PrepareStmt:            true,
		SkipDefaultTransaction: true,
	})
	handleError(err)

	sqlDB, err := db.DB()
	handleError(err)

	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	handleError(db.AutoMigrate(&Link{}, &ClickEvent{}))
	return db
}

func setupServer(db *gorm.DB) {
	server := &http.Server{
		Addr:         ":8080",
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
		Handler:      handler(db),
	}

	log.Fatal(server.ListenAndServe())
}

func handler(db *gorm.DB) http.Handler {
	var (
		linkCache sync.Map
		mu        sync.Mutex
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			handleRootRequest(w, r, db)
			return
		}

		id := r.URL.Path[1:]
		if cached, ok := linkCache.Load(id); ok {
			handleCachedLink(w, r, cached.(*Link), db)
			return
		}

		handleUncachedLink(w, r, id, db, &linkCache, &mu)
	})
}

func handleRootRequest(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	switch r.Method {
	case http.MethodGet:
		var listURL []Link
		db.Find(&listURL)

		var topReferrers []struct {
			Referer string
			Count   int
		}

		db.Model(&ClickEvent{}).
			Select("referer, count(*) as count").
			Group("referer").
			Order("count desc").
			Limit(3).
			Scan(&topReferrers)

		renderTemplate(w, "register.html", struct {
			ListURL      []Link
			TopReferrers []struct {
				Referer string
				Count   int
			}
		}{listURL, topReferrers})

	case http.MethodPost:
		url := r.FormValue("url")
		if url == "" {
			http.Error(w, "URL is required", http.StatusBadRequest)
			return
		}

		newLink := createLink(url, db, &sync.Mutex{})
		if newLink == nil {
			http.Error(w, "Failed to create short link", http.StatusInternalServerError)
			return
		}

		renderTemplate(w, "success.html", struct{ ID, URL string }{
			newLink.ID, newLink.URL,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleCachedLink(w http.ResponseWriter, r *http.Request, link *Link, db *gorm.DB) {
	recordClickEvent(r, link.ID, db)
	http.Redirect(w, r, link.URL, http.StatusSeeOther)
	updateClickCount(link, db)
}

func handleUncachedLink(w http.ResponseWriter, r *http.Request, id string,
	db *gorm.DB, cache *sync.Map, mu *sync.Mutex,
) {
	var link Link
	if err := db.First(&link, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			renderTemplate(w, "register.html", nil)
			return
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	mu.Lock()
	cache.Store(id, &link)
	mu.Unlock()

	recordClickEvent(r, id, db)
	http.Redirect(w, r, link.URL, http.StatusSeeOther)
	updateClickCount(&link, db)
}

func recordClickEvent(r *http.Request, linkID string, db *gorm.DB) {
	db.Create(&ClickEvent{
		LinkID:    linkID,
		Referer:   r.Header.Get("Referer"),
		Timestamp: time.Now(),
	})
}

func updateClickCount(link *Link, db *gorm.DB) {
	db.Model(link).Update("click_count", gorm.Expr("click_count + 1"))
}

func renderTemplate(w http.ResponseWriter, tmpl string, data interface{}) {
	tpl := templatePool.Get().(*template.Template)
	defer templatePool.Put(tpl)

	err := tpl.ExecuteTemplate(w, tmpl, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func handleError(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
