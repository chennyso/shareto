package httpd

import (
	"fmt"
	"html/template"
	"io"
)

type SharePageData struct {
	Token              string
	FileName           string
	FileSizeFormatted  string
	RemainingDownloads int
	ExpiresAtFormatted string
	HasPassword        bool
	CSRFToken          string
	ErrorMessage       string
}

type ErrorPageData struct {
	StatusCode int
	Title      string
	Message    string
}

// FormatBytes formats byte sizes into human-readable strings (e.g. 4.28 GB).
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

const landingTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Download {{.FileName}} - Shareto</title>
  <style>
    :root {
      --bg: #0f172a;
      --card-bg: #1e293b;
      --border: #334155;
      --text: #f8fafc;
      --muted: #94a3b8;
      --primary: #38bdf8;
      --primary-hover: #0284c7;
      --error-bg: #450a0a;
      --error-text: #f87171;
      --error-border: #991b1b;
    }
    @media (prefers-color-scheme: light) {
      :root {
        --bg: #f8fafc;
        --card-bg: #ffffff;
        --border: #e2e8f0;
        --text: #0f172a;
        --muted: #64748b;
        --primary: #0284c7;
        --primary-hover: #0369a1;
        --error-bg: #fef2f2;
        --error-text: #dc2626;
        --error-border: #fecaca;
      }
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      background-color: var(--bg);
      color: var(--text);
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
      padding: 1.5rem;
    }
    .card {
      background-color: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 12px;
      padding: 2rem;
      max-width: 480px;
      width: 100%;
      box-shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.1), 0 2px 4px -2px rgba(0, 0, 0, 0.1);
    }
    .brand {
      font-size: 0.875rem;
      font-weight: 600;
      color: var(--primary);
      text-transform: uppercase;
      letter-spacing: 0.05em;
      margin-bottom: 0.75rem;
    }
    .filename {
      font-size: 1.25rem;
      font-weight: 700;
      word-break: break-all;
      margin-bottom: 1.25rem;
      line-height: 1.4;
    }
    .meta-list {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 0.75rem;
      margin-bottom: 1.5rem;
      padding: 1rem;
      background: rgba(125, 125, 125, 0.05);
      border-radius: 8px;
    }
    .meta-item { display: flex; flex-direction: column; }
    .meta-label { font-size: 0.75rem; color: var(--muted); margin-bottom: 0.25rem; }
    .meta-value { font-size: 0.95rem; font-weight: 600; }
    .alert {
      padding: 0.75rem 1rem;
      background-color: var(--error-bg);
      border: 1px solid var(--error-border);
      color: var(--error-text);
      border-radius: 6px;
      font-size: 0.875rem;
      margin-bottom: 1.25rem;
    }
    .form-group {
      margin-bottom: 1.25rem;
    }
    label {
      display: block;
      font-size: 0.875rem;
      font-weight: 500;
      margin-bottom: 0.5rem;
      color: var(--text);
    }
    input[type="password"] {
      width: 100%;
      padding: 0.625rem 0.875rem;
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 6px;
      color: var(--text);
      font-size: 0.95rem;
      outline: none;
      transition: border-color 0.2s;
    }
    input[type="password"]:focus {
      border-color: var(--primary);
    }
    button.btn {
      width: 100%;
      padding: 0.75rem;
      background-color: var(--primary);
      color: #ffffff;
      border: none;
      border-radius: 6px;
      font-size: 1rem;
      font-weight: 600;
      cursor: pointer;
      transition: background-color 0.2s;
    }
    button.btn:hover {
      background-color: var(--primary-hover);
    }
    .footer {
      margin-top: 1.5rem;
      text-align: center;
      font-size: 0.75rem;
      color: var(--muted);
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="brand">Shareto Secure Share</div>
    <div class="filename">{{.FileName}}</div>

    {{if .ErrorMessage}}
    <div class="alert">{{.ErrorMessage}}</div>
    {{end}}

    <div class="meta-list">
      <div class="meta-item">
        <span class="meta-label">File Size</span>
        <span class="meta-value">{{.FileSizeFormatted}}</span>
      </div>
      <div class="meta-item">
        <span class="meta-label">Remaining Downloads</span>
        <span class="meta-value">{{.RemainingDownloads}}</span>
      </div>
      <div class="meta-item" style="grid-column: span 2;">
        <span class="meta-label">Expires At</span>
        <span class="meta-value">{{.ExpiresAtFormatted}}</span>
      </div>
    </div>

    <form method="POST" action="/s/{{.Token}}/claim">
      <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
      {{if .HasPassword}}
      <div class="form-group">
        <label for="password">Password Required</label>
        <input type="password" id="password" name="password" placeholder="Enter share password" required autofocus>
      </div>
      {{end}}
      <button type="submit" class="btn">Download File</button>
    </form>

    <div class="footer">
      End-to-End verified &middot; Expired sessions are wiped automatically
    </div>
  </div>
</body>
</html>`

const errorTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{.StatusCode}} - {{.Title}}</title>
  <style>
    :root {
      --bg: #0f172a;
      --card-bg: #1e293b;
      --border: #334155;
      --text: #f8fafc;
      --muted: #94a3b8;
      --primary: #38bdf8;
    }
    @media (prefers-color-scheme: light) {
      :root {
        --bg: #f8fafc;
        --card-bg: #ffffff;
        --border: #e2e8f0;
        --text: #0f172a;
        --muted: #64748b;
        --primary: #0284c7;
      }
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      background-color: var(--bg);
      color: var(--text);
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
      padding: 1.5rem;
    }
    .card {
      background-color: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 12px;
      padding: 2rem;
      max-width: 440px;
      width: 100%;
      text-align: center;
      box-shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.1);
    }
    .code { font-size: 3rem; font-weight: 800; color: var(--primary); margin-bottom: 0.5rem; }
    .title { font-size: 1.25rem; font-weight: 700; margin-bottom: 0.75rem; }
    .message { font-size: 0.95rem; color: var(--muted); line-height: 1.5; }
  </style>
</head>
<body>
  <div class="card">
    <div class="code">{{.StatusCode}}</div>
    <div class="title">{{.Title}}</div>
    <div class="message">{{.Message}}</div>
  </div>
</body>
</html>`

var (
	landingTmpl = template.Must(template.New("landing").Parse(landingTemplateHTML))
	errorTmpl   = template.Must(template.New("error").Parse(errorTemplateHTML))
)

func RenderLandingPage(w io.Writer, data SharePageData) error {
	return landingTmpl.Execute(w, data)
}

func RenderErrorPage(w io.Writer, data ErrorPageData) error {
	return errorTmpl.Execute(w, data)
}
