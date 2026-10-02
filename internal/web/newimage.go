package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/kumaranik/milesahead/internal/db"
)

// handleRequestNewImage is "Find a new image": the admin turns down the
// article's image and, optionally, says what to search for instead. A
// rewrite never touches the image, so asking for a different picture in
// rewrite feedback got new text and the same picture.
//
// Nothing changes on the article yet. The enrichment reconciler takes the
// request on its next pass (every 15 minutes), first in line and with a
// fresh attempt budget, and swaps the new image in only once it has found
// one; until then, and if it finds nothing, the article -- live or not --
// keeps the image it has. The image turned down is never picked for it again.
//
// Any article but a rejected one can have a new image, published included.
func (app *App) handleRequestNewImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}
	if _, err := app.db.RequestNewImage(id, r.FormValue("hint")); err != nil {
		if errors.Is(err, db.ErrNoImageRequest) {
			http.Error(w, "This article cannot have a new image: it does not exist or was rejected", http.StatusBadRequest)
			return
		}
		app.serverError(w, r, "admin: failed to request a new image", err)
		return
	}
	if r.FormValue("return") == "published" {
		http.Redirect(w, r, withSort(r, fmt.Sprintf("/admin/published?msg=image_requested&id=%d&sent=image-%d", id, id), publishedSorts), http.StatusSeeOther)
		return
	}
	// Back to the article's preview, which shows the request.
	http.Redirect(w, r, withSort(r, fmt.Sprintf("/admin/dashboard?msg=image_requested&id=%d&open=%d&sent=image-%d", id, id, id), queueSorts), http.StatusSeeOther)
}

// imageRequestedMessage is the banner after "Find a new image".
const imageRequestedMessage = "Looking for a new image. The current one stays up until a replacement is found, usually within 15 minutes."

// attachImageRequests loads the image requests of rows, in one query.
func (app *App) attachImageRequests(rows []adminArticleRow) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	reqs, err := app.db.ImageRequestsFor(ids)
	if err != nil {
		slog.Warn("admin: could not load image requests", "error", err)
		return
	}
	for i := range rows {
		rows[i].ImageRequest = reqs[rows[i].ID]
	}
}
