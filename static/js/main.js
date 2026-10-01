// Theme Toggle
// layout.html applies the saved theme (or the OS setting) in <head> before first
// paint; this only wires up the buttons (one in the header, one in the footer
// for phones) and keeps their pressed state in sync.
(function() {
    const html = document.documentElement;
    const saved = localStorage.getItem('theme');
    if (saved) {
        html.setAttribute('data-theme', saved);
    }

    document.addEventListener('DOMContentLoaded', function() {
        const buttons = document.querySelectorAll('[data-theme-toggle]');
        const sync = function() {
            const dark = html.getAttribute('data-theme') === 'dark';
            buttons.forEach(function(b) { b.setAttribute('aria-pressed', dark ? 'true' : 'false'); });
        };
        sync();
        buttons.forEach(function(b) {
            b.addEventListener('click', function() {
                const next = html.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
                html.setAttribute('data-theme', next);
                localStorage.setItem('theme', next);
                sync();
            });
        });
    });
})();

// Vote Handler
// Matches the article template's .vote-buttons group; data-vote is "up" or
// "down", which is what /api/vote accepts.
document.addEventListener('DOMContentLoaded', function() {
    var group = document.querySelector('.vote-buttons[data-article-id]');
    if (!group) return;

    var card = group.closest('.vote-card');
    var status = card ? card.querySelector('.vote-status') : null;
    var articleId = parseInt(group.getAttribute('data-article-id'), 10);
    var votedKey = 'voted_' + articleId;
    var btns = group.querySelectorAll('.vote-btn');

    function finish(choice, message) {
        btns.forEach(function(b) {
            var chosen = b.getAttribute('data-vote') === choice;
            b.disabled = true;
            b.setAttribute('aria-pressed', chosen ? 'true' : 'false');
            b.classList.toggle('active', chosen);
        });
        if (status) status.textContent = message;
    }

    var previous = localStorage.getItem(votedKey);
    if (previous) {
        finish(previous, 'Thanks for your feedback.');
        return;
    }

    group.addEventListener('click', function(e) {
        var btn = e.target.closest('.vote-btn');
        if (!btn || btn.disabled) return;
        var choice = btn.getAttribute('data-vote');
        btns.forEach(function(b) { b.disabled = true; });

        fetch('/api/vote', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ article_id: articleId, vote: choice })
        })
        .then(function(res) {
            if (res.status === 409) {
                // Voted before from this network; which way is not known here.
                localStorage.setItem(votedKey, 'voted');
                finish('', "You've already voted on this article. Thanks!");
                return null;
            }
            if (!res.ok) throw new Error('HTTP ' + res.status);
            return res.json();
        })
        .then(function(data) {
            if (!data) return;
            var upEl = group.querySelector('.upvotes');
            var downEl = group.querySelector('.downvotes');
            if (upEl) upEl.textContent = data.upvotes;
            if (downEl) downEl.textContent = data.downvotes;
            localStorage.setItem(votedKey, choice);
            finish(choice, 'Thanks for your feedback.');
        })
        .catch(function() {
            btns.forEach(function(b) { b.disabled = false; });
            if (status) status.textContent = "Sorry, your vote didn't go through. Please try again.";
        });
    });
});

// In the admin dashboard's article preview, links in the story open in a new
// tab so following one does not leave the review. Public article pages are
// left alone: the server already opens links to other sites in a new tab and
// keeps links within the site (card pages, other articles) in the same tab.
document.addEventListener('DOMContentLoaded', function() {
    document.querySelectorAll('.preview-modal .article-content a').forEach(function(link) {
        link.setAttribute('target', '_blank');
        link.setAttribute('rel', 'noopener');
    });
});

// Article share: "Copy link" is in the page hidden, and shown only where the
// clipboard API exists. The status line confirms the copy.
document.addEventListener('DOMContentLoaded', function() {
    var btn = document.querySelector('.share-copy[data-share-url]');
    if (!btn || !navigator.clipboard || !window.isSecureContext) return;
    var status = document.querySelector('.article-share .share-status');
    btn.hidden = false;
    btn.addEventListener('click', function() {
        navigator.clipboard.writeText(btn.getAttribute('data-share-url')).then(function() {
            if (status) status.textContent = 'Link copied.';
        }, function() {
            if (status) status.textContent = "Couldn't copy the link.";
        });
    });
});

// Search Toggle
// The button shows and hides the header search field. aria-expanded follows
// it; Escape or a click elsewhere closes it, and Escape returns focus to the
// button.
document.addEventListener('DOMContentLoaded', function() {
    var searchToggle = document.getElementById('search-toggle');
    var searchForm = document.getElementById('nav-search-form');
    if (!searchToggle || !searchForm) return;
    var input = searchForm.querySelector('input');

    function setOpen(open) {
        searchForm.classList.toggle('active', open);
        searchToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
    }

    searchToggle.addEventListener('click', function(e) {
        e.stopPropagation();
        var open = !searchForm.classList.contains('active');
        setOpen(open);
        if (open) input.focus();
    });

    document.addEventListener('click', function(e) {
        if (searchForm.classList.contains('active') && !searchForm.contains(e.target) && e.target !== searchToggle) {
            setOpen(false);
        }
    });

    document.addEventListener('keydown', function(e) {
        if (e.key === 'Escape' && searchForm.classList.contains('active')) {
            setOpen(false);
            searchToggle.focus();
        }
    });
});

// Cards index: the sort menu opens the chosen order as soon as it changes,
// so its Go button (there for browsers without JavaScript) is hidden. Each
// option carries its page's URL, which keeps the filters and drops a default
// sort from the URL. The "No annual fee" toggle is a link announced as a
// toggle button, so Space presses it as it would a button.
(function() {
    var menu = document.querySelector('[data-sort-menu]');
    if (menu) {
        var go = menu.form && menu.form.querySelector('.cards-sort-go');
        if (go) go.hidden = true;
        menu.addEventListener('change', function() {
            var url = menu.options[menu.selectedIndex].getAttribute('data-url');
            if (url) window.location.assign(url);
        });
    }
    document.querySelectorAll('a.filter-toggle[role="button"]').forEach(function(a) {
        a.addEventListener('keydown', function(e) {
            if (e.key === ' ') {
                e.preventDefault();
                a.click();
            }
        });
    });
})();
