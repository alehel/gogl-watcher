package gog

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// User is the authenticated GOG account.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// ListedGame is one entry of the account game list.
type ListedGame struct {
	ID           int64
	Title        string
	Slug         string
	Image        string
	WorksWindows bool
	WorksMac     bool
	WorksLinux   bool
}

// Product is the detailed view of a game or DLC including its downloads.
type Product struct {
	ID           int64     `json:"id"`
	Title        string    `json:"title"`
	Slug         string    `json:"slug"`
	Downloads    Downloads `json:"downloads"`
	ExpandedDLCs []Product `json:"expanded_dlcs"`
	Images       struct {
		Background string `json:"background"`
		Logo       string `json:"logo"`
		Logo2x     string `json:"logo2x"`
		Icon       string `json:"icon"`
	} `json:"images"`
}

// Downloads groups the downloadable items of a product.
type Downloads struct {
	Installers   []Installer `json:"installers"`
	Patches      []Installer `json:"patches"`
	BonusContent []Bonus     `json:"bonus_content"`
}

// Installer is an offline installer for one OS and language, possibly split in parts.
type Installer struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	OS           string         `json:"os"`
	Language     string         `json:"language"`
	LanguageFull string         `json:"language_full"`
	Version      string         `json:"version"`
	TotalSize    FlexInt        `json:"total_size"`
	Files        []DownloadFile `json:"files"`
}

// Bonus is extra content such as a soundtrack or manual.
type Bonus struct {
	ID        FlexString     `json:"id"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Count     FlexInt        `json:"count"`
	TotalSize FlexInt        `json:"total_size"`
	Files     []DownloadFile `json:"files"`
}

// DownloadFile is one file of an installer or bonus item.
type DownloadFile struct {
	ID       FlexString `json:"id"`
	Size     FlexInt    `json:"size"`
	Downlink string     `json:"downlink"`
}

// Build is one build of a product in GOG's Galaxy content system. It describes
// the chunked depot install, not the offline installer this application
// downloads, so it is only used as a hint that something was rebuilt.
type Build struct {
	ID          string
	VersionName string
	PublishedAt time.Time
	Public      bool
}

// Downlink is the resolved, time-limited CDN location of a file.
type Downlink struct {
	URL         string `json:"downlink"`
	ChecksumURL string `json:"checksum"`
}

// Checksum is the content of GOG's per-file checksum XML.
type Checksum struct {
	Name      string `xml:"name,attr"`
	MD5       string `xml:"md5,attr"`
	TotalSize int64  `xml:"total_size,attr"`
}

// FlexInt decodes JSON numbers or numeric strings.
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = FlexInt(n)
		return nil
	}
	fl, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = FlexInt(int64(fl))
	return nil
}

// FlexString decodes JSON strings or numbers into a string.
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	*f = FlexString(string(b))
	return nil
}

// OwnedSet is a set of owned product ids.
type OwnedSet map[int64]bool
