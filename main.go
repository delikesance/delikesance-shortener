package main

import (
	"html/template"
	"log"
	"net/http"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func incrementString(s string) string {
	if s == "" {
		return "0"
	}

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
		case (chars[i] >= '0' && chars[i] < '9') || (chars[i] >= 'a' && chars[i] < 'z') || (chars[i] >= 'A' && chars[i] < 'Z'):
			chars[i]++
			return string(chars)
		}
	}
	return "0" + string(chars)
}

type Link struct {
	ID        string `gorm:"primaryKey"`
	URL       string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func createLink(url string, db *gorm.DB) *Link {
	var lastLink Link
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Order("id DESC").First(&lastLink).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			lastLink.ID = "0000"
		} else {
			log.Fatal(err)
		}
	}

	newLink := &Link{
		ID:  incrementString(lastLink.ID),
		URL: url,
	}

	if err := db.Create(newLink).Error; err != nil {
		log.Fatal(err)
	}

	return newLink
}

func main() {
	db, err := gorm.Open(sqlite.Open("gorm.db"), &gorm.Config{
		PrepareStmt: true,
	})

	handleError(err)

	if err := db.AutoMigrate(&Link{}); err != nil {
		log.Fatal(err)
	}

	// web server
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if r.Method == "GET" {
				var listURL []Link
				db.Find(&listURL)

				renderTemplate(w, "register.html", struct{ ListURL []Link }{listURL})
			} else if r.Method == "POST" {
				url := r.FormValue("url")
				newLink := createLink(url, db)
				renderTemplate(w, "success.html", struct{ ID, URL string }{newLink.ID, newLink.URL})
			}
			return
		}

		id := r.URL.Path[1:]
		var link Link
		if err := db.First(&link, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				renderTemplate(w, "register.html", nil)
				return
			}
			log.Fatal(err)
		}

		http.Redirect(w, r, link.URL, http.StatusSeeOther)
	})

	handleError(http.ListenAndServe(":8080", nil))
}

func handleError(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func renderTemplate(w http.ResponseWriter, tmpl string, data interface{}) {
	t, err := template.ParseFiles(tmpl)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = t.Execute(w, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
