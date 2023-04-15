package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/PuerkitoBio/goquery"
	"github.com/jeromenerf/feeds"
	"golang.org/x/text/unicode/norm"
)

func main() {

	bookchan := make(chan string)
	donechan := make(chan string)

	allbooksurl := "https://www.litteratureaudio.com/classement-de-nos-livres-audio-gratuits-les-plus-apprecies"
	bookurls, err := GetBookUrls(allbooksurl)
	if err != nil {
		log.Fatalf("[ERROR]", err)
	}
	// 	bookurls := []string{
	// 		"http://www.litteratureaudio.com/livre-audio-gratuit-mp3/conan-doyle-arthur-une-etude-en-rouge.html",
	// 		"http://www.litteratureaudio.com/livre-audio-gratuit-mp3/conan-doyle-arthur-un-scandale-en-boheme.html"}
	// Then for each book, create an RSS feed

	// Launch workers
	for i := 0; i < runtime.GOMAXPROCS(runtime.NumCPU()); i++ {
		go CreateFeedRoutine(bookchan, donechan)
	}

	// Send work to workers
	go func() {
		for _, bookurl := range bookurls {
			bookchan <- bookurl
		}
		close(bookchan)
	}()

	// Monitor workers output
	for b := range bookurls {
		bookurl := <-donechan
		log.Printf("%5d / %5d : %s", b, len(bookurls), bookurl)
	}
}

// GetBooksUrls parse a page for all specific books url
func GetBookUrls(allbooksurl string) (bookurls []string, err error) {

	maxpg := 277

	// Get the full list of audio books
	for pg := 1; pg < maxpg; pg++ {
		booksurl := fmt.Sprintf(allbooksurl+"/page/%d", pg)
		log.Printf("Getting books at %s", booksurl)
		res, err := http.Get(booksurl)
		if err != nil {
			log.Println(err)
			return bookurls, err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			log.Printf("status code error: %d %s", res.StatusCode, res.Status)
			return bookurls, err
		}
		doc, err := goquery.NewDocumentFromReader(res.Body)
		if err != nil {
			log.Println(err)
			return bookurls, err
		}

		doc.Find("article header h3.entry-title a").Each(func(i int, s *goquery.Selection) {
			bookurl, _ := s.Attr("href")
			log.Printf("Found %s", bookurl)
			bookurls = append(bookurls, bookurl)
		})
	}
	return bookurls, err
}

// CreateFeedRoutine is our worker
func CreateFeedRoutine(in, out chan string) {
	for {
		bookurl, more := <-in
		if more {
			err := CreateFeedFromAPI(bookurl)
			if err != nil {
				log.Println("[ERROR]", err)
			}
			out <- bookurl
		} else {
			return
		}
	}
}

type Station struct {
	Title struct {
		Rendered string `json:"rendered"`
	} `json:"title"`
	Meta struct {
		Stream string `json:"stream"`
	} `json:"meta"`
}

// CreateFeedFromAPI fetches the bookurl and generates an RSS feed on disk, based on the book name
func CreateFeedFromAPI(bookurl string) (err error) {
	feed := &feeds.Feed{
		Link: &feeds.Link{Href: bookurl},
	}
	filename := ""

	// Get a book URL
	res, err := http.Get(bookurl)
	if err != nil {
		log.Println(err)
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		log.Printf("status code error: %d %s", res.StatusCode, res.Status)
		return err
	}

	// Parse the page
	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		log.Println(err)
		return err
	}

	// Extract the book name and author
	feed.Title = strings.TrimSpace(doc.Find("article.post .header-station > .entry-header h1.entry-title").Text())
	feed.Description = feed.Title
	feed.Language = "fr"
	feed.Author = &feeds.Author{Name: strings.TrimSpace(doc.Find("article.post .header-station > .entry-header .entry-auteur a").Text())}
	filename = Clean(feed.Author.Name + " " + feed.Title)

	if filename == "" {
		err = fmt.Errorf("[ERROR] %s has no title", bookurl)
		log.Println(err)
		return err
	}

	// Extract the cover image for greatness
	coverurl, _ := doc.Find("article.post .header-station .post-thumbnail img").Attr("src")
	if coverurl != "" {
		feed.Image = &feeds.Image{Url: coverurl}
	}

	// Extract all the chapters name and mp3 URL
	doc.Find("article.album-track").Each(func(i int, s *goquery.Selection) {
		id, _ := s.Attr("data-play-id")

		res, err := http.Get(fmt.Sprintf("https://www.litteratureaudio.com/wp-json/wp/v2/station/%s?_fields=title,meta.stream,media.download_url", id))
		if err != nil {
			log.Println(err)
			return
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			log.Printf("status code error: %d %s", res.StatusCode, res.Status)
			return
		}
		var station Station
		body, err := ioutil.ReadAll(res.Body)
		if err != nil {
			log.Printf("Error reading body: %s", err.Error())
			return
		}
		json.Unmarshal(body, &station)

		name := station.Title.Rendered
		url := station.Meta.Stream
		item := &feeds.Item{
			Title: name,
			Link:  &feeds.Link{Href: url},
			Enclosure: &feeds.Enclosure{
				Url:    url,
				Type:   "audio/mpeg",
				Length: "1000000",
			},
			Description: name,
			Created:     time.Now().Add(time.Duration(24*i) * time.Hour), // adding 24 hours in between to help with players ordering
		}
		feed.Items = append(feed.Items, item)
	})

	// Create the RSS feed
	rss, err := feed.ToRss()
	if err != nil {
		log.Println(err)
		return err
	}

	// Write RSS feed to file
	f, err := os.Create(filename + ".rss")
	if err != nil {
		log.Println(err)
		return err
	}
	defer f.Close()
	_, err = f.WriteString(rss)
	if err != nil {
		log.Println(err)
		return err
	}

	return err

}

// CreateFeedFromPage fetches the bookurl and generates an RSS feed on disk, based on the book name
func CreateFeedFromPage(bookurl string) (err error) {
	feed := &feeds.Feed{
		Link: &feeds.Link{Href: bookurl},
	}
	filename := ""

	// Get a book URL
	res, err := http.Get(bookurl)
	if err != nil {
		log.Println(err)
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		log.Printf("status code error: %d %s", res.StatusCode, res.Status)
		return err
	}

	// Parse the page
	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		log.Println(err)
		return err
	}

	// Extract the book name and author
	feed.Title = strings.TrimSpace(doc.Find("article.post .header-station > .entry-header h1.entry-title").Text())
	feed.Description = feed.Title
	feed.Language = "fr"
	feed.Author = &feeds.Author{Name: strings.TrimSpace(doc.Find("article.post .header-station > .entry-header .entry-auteur a").Text())}
	filename = Clean(feed.Author.Name + " " + feed.Title)

	if filename == "" {
		err = fmt.Errorf("[ERROR] %s has no title", bookurl)
		log.Println(err)
		return err
	}

	// Extract the cover image for greatness
	coverurl, _ := doc.Find("article.post .header-station .post-thumbnail img").Attr("src")
	if coverurl != "" {
		feed.Image = &feeds.Image{Url: coverurl}
	}

	// Extract all the chapters name and mp3 URL
	doc.Find("article.album-track").Each(func(i int, s *goquery.Selection) {
		name := s.Find(".entry-header .entry-title").Text()
		url, _ := s.Find(".entry-footer a.no-ajax").Attr("href")
		item := &feeds.Item{
			Title: name,
			Link:  &feeds.Link{Href: url},
			Enclosure: &feeds.Enclosure{
				Url:    url,
				Type:   "audio/mpeg",
				Length: "1000000",
			},
			Description: name,
			Created:     time.Now().Add(time.Duration(24*i) * time.Hour), // adding 24 hours in between to help with players ordering
		}
		feed.Items = append(feed.Items, item)
	})

	// Create the RSS feed
	rss, err := feed.ToRss()
	if err != nil {
		log.Println(err)
		return err
	}

	// Write RSS feed to file
	f, err := os.Create(filename + ".rss")
	if err != nil {
		log.Println(err)
		return err
	}
	defer f.Close()
	_, err = f.WriteString(rss)
	if err != nil {
		log.Println(err)
		return err
	}

	return err

}

var (
	// Replace non-alphanumeric characters with this byte.
	Replacement = '_'

	// The "safe" set of characters.
	alphanum = &unicode.RangeTable{
		R16: []unicode.Range16{
			{0x0030, 0x0039, 1}, // 0-9
			{0x0041, 0x005A, 1}, // A-Z
			{0x0061, 0x007A, 1}, // a-z
		},
	}
	// Characters in these ranges will be ignored.
	nop = []*unicode.RangeTable{
		unicode.Mark,
		unicode.Sk, // Symbol - modifier
		unicode.Lm, // Letter - modifier
		unicode.Cc, // Other - control
		unicode.Cf, // Other - format
	}
)

// Slug replaces each run of characters which are not ASCII letters or numbers
// with the Replacement character, except for leading or trailing runs. Letters
// will be stripped of diacritical marks and lowercased. Letter or number
// codepoints that do not have combining marks or a lower-cased variant will be
// passed through unaltered.
func Clean(s string) string {
	buf := make([]rune, 0, len(s))
	replacement := false

	for _, r := range norm.NFKD.String(s) {
		switch {
		case unicode.In(r, alphanum):
			buf = append(buf, unicode.ToLower(r))
			replacement = true
		case unicode.IsOneOf(nop, r):
			// skip
		case replacement:
			buf = append(buf, Replacement)
			replacement = false
		}
	}

	// Strip trailing Replacement byte
	if i := len(buf) - 1; i >= 0 && buf[i] == Replacement {
		buf = buf[:i]
	}

	return string(buf)
}
