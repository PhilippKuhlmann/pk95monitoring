package notify

// Details carry the structured part of an event: shown in the UI and rendered into the mail.
type Details struct {
	// Status of the whole thing: ok (green), warning (yellow), error (red). Empty = from severity.
	Status string `json:"status,omitempty"`
	// Facts are label/value pairs (cluster, target, duration …).
	Facts []Fact `json:"facts,omitempty"`
	// RowsTitle names the table, e.g. "Gesicherte VMs und Container".
	RowsTitle string `json:"rows_title,omitempty"`
	Rows      []Row  `json:"rows,omitempty"`
	// Log is an excerpt of a task log (errors).
	Log []string `json:"log,omitempty"`
	// Link points into the web UI (path, the mail adds the address).
	Link string `json:"link,omitempty"`
	// Images are shown in the mail (inline attachments with these content IDs).
	Images []Image `json:"images,omitempty"`
	// Note is a closing remark, e.g. "Der vollständige Bericht hängt an."
	Note string `json:"note,omitempty"`
	// CountWords label the counts of rows above the table (ok, warning, error); empty =
	// "erfolgreich", "mit Warnung", "fehlgeschlagen".
	CountWords [3]string `json:"-"`
}

// Image is a picture in the mail.
type Image struct {
	CID     string `json:"cid"`
	Caption string `json:"caption"`
	Status  string `json:"status,omitempty"`
}

// Fact is one label/value pair.
type Fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Row is one guest (or disk) in the table.
type Row struct {
	Kind     string `json:"kind,omitempty"` // VM, CT
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Status   string `json:"status"` // ok, warning, error
	Time     int64  `json:"time,omitempty"`
	Duration string `json:"duration,omitempty"`
	Size     string `json:"size,omitempty"`
	Info     string `json:"info,omitempty"`
}

// StatusOf returns the effective status: the explicit one or the one of the severity.
func (d *Details) StatusOf(severity string) string {
	if d != nil && d.Status != "" {
		return d.Status
	}
	switch severity {
	case "critical":
		return "error"
	case "warning":
		return "warning"
	}
	return "ok"
}
