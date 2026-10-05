// Package content serves the two read-only pages at the bottom of Profil Saya:
// Pusat Bantuan (FAQ) and Kontak CS.
//
// It is a domain of its own rather than a corner of account because none of it
// is customer data. Both endpoints answer the same bytes to everyone and need
// no Authorization at all — a customer locked out of the app is exactly the one
// who needs the CS number.
package content

// HelpItem is one question and its answer.
type HelpItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// HelpCategory groups items under one heading. Key is what the client maps to
// an icon; Title is sent alongside so a client that does not recognise a new
// key still has something to print instead of the raw key.
type HelpCategory struct {
	Key   string     `json:"key"`
	Title string     `json:"title"`
	Items []HelpItem `json:"items"`
}

// HelpCenterResponse is the body of GET /v1/content/help-center.
type HelpCenterResponse struct {
	Categories []HelpCategory `json:"categories"`
}

// ContactCS is the body of GET /v1/content/contact-cs.
//
// Plain strings, already formatted for display. The client dials PhoneFree from
// abroad and Phone locally; parsing a single field into two would put that rule
// in the app, where changing it means a release.
type ContactCS struct {
	Phone     string `json:"phone"`
	PhoneFree string `json:"phone_free"`
	WhatsApp  string `json:"whatsapp"`
	Email     string `json:"email"`
	ChatURL   string `json:"chat_url"`
	Hours     string `json:"hours"`
}
