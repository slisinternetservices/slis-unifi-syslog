package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

func (s *Server) getBranding(w http.ResponseWriter, r *http.Request) { jsonOut(w, s.Meta.Branding()) }

func (s *Server) putBranding(w http.ResponseWriter, r *http.Request) {
	var b store.Branding
	if err := decode(r, &b); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	if len(b.CompanyName) > 120 {
		jerr(w, 400, "the company name may be at most 120 characters")
		return
	}
	if err := s.Meta.SaveBranding(b); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "settings_branding", strings.TrimSpace(b.CompanyName))
	jsonOut(w, s.Meta.Branding())
}

// putBrandingAsset takes the raw image as the request body.
func (s *Server) putBrandingAsset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "asset")
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, store.MaxBrandingAsset+1))
	if err != nil {
		jerr(w, 400, "the image may be at most 512 KB")
		return
	}
	if _, err := s.Meta.SaveBrandingAsset(name, data); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			jerr(w, 404, "not found")
		} else {
			jerr(w, 400, err.Error())
		}
		return
	}
	s.audit_(r, "branding_upload", name)
	jsonOut(w, s.Meta.Branding())
}

func (s *Server) deleteBrandingAsset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "asset")
	if err := s.Meta.DeleteBrandingAsset(name); err != nil {
		jerr(w, 404, "not found")
		return
	}
	s.audit_(r, "branding_delete", name)
	jsonOut(w, s.Meta.Branding())
}

// brandingAsset serves an uploaded image publicly, since the sign-in page
// shows it. Uploaded SVG could carry script, so the response is sandboxed:
// opened directly it cannot run anything, and <img> never runs scripts.
func (s *Server) brandingAsset(w http.ResponseWriter, r *http.Request) {
	a, err := s.Meta.BrandingAsset(chi.URLParam(r, "asset"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.Type)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("ETag", `"`+a.Hash+`"`)
	// URLs carry ?v=<version>, so a changed image gets a new URL.
	h.Set("Cache-Control", "public, max-age=86400")
	if strings.Contains(r.Header.Get("If-None-Match"), a.Hash) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(a.Data)
}
