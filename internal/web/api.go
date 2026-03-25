package web

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/kumaranik/milesahead/internal/vote"
)

func (app *App) handleVoteAPI(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ArticleID int64  `json:"article_id"`
		Vote      string `json:"vote"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid request"}`)
		return
	}

	if req.Vote != "up" && req.Vote != "down" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"vote must be up or down"}`)
		return
	}

	ip := extractIP(r)
	ipHash := fmt.Sprintf("%x", sha256.Sum256([]byte(ip)))

	err := app.voteSvc.CastVote(req.ArticleID, req.Vote, ipHash)
	if err != nil {
		if errors.Is(err, vote.ErrAlreadyVoted) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":"already voted"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"failed to record vote"}`)
		return
	}

	counts, err := app.voteSvc.GetCounts(req.ArticleID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"failed to get counts"}`)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"upvotes":   counts.Up,
		"downvotes": counts.Down,
	})
}
