// Theme Toggle
// layout.html applies the saved theme (or the OS setting) in <head> before first
// paint; this only wires up the button and keeps its pressed state in sync.
(function() {
    const html = document.documentElement;
    const saved = localStorage.getItem('theme');
    if (saved) {
        html.setAttribute('data-theme', saved);
    }

    document.addEventListener('DOMContentLoaded', function() {
        const toggleBtn = document.getElementById('theme-toggle');
        if (toggleBtn) {
            const sync = function() {
                toggleBtn.setAttribute('aria-pressed', html.getAttribute('data-theme') === 'dark' ? 'true' : 'false');
            };
            sync();
            toggleBtn.addEventListener('click', function() {
                const current = html.getAttribute('data-theme');
                const next = current === 'dark' ? 'light' : 'dark';
                html.setAttribute('data-theme', next);
                localStorage.setItem('theme', next);
                sync();
            });
        }
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
document.addEventListener('DOMContentLoaded', function() {
    var searchToggle = document.getElementById('search-toggle');
    var searchForm = document.getElementById('nav-search-form');
    if (!searchToggle || !searchForm) return;

    searchToggle.addEventListener('click', function(e) {
        e.stopPropagation();
        searchForm.classList.toggle('active');
        if (searchForm.classList.contains('active')) {
            searchForm.querySelector('input').focus();
        }
    });

    // Close search form when clicking outside.
    document.addEventListener('click', function(e) {
        if (!searchForm.contains(e.target) && e.target !== searchToggle) {
            searchForm.classList.remove('active');
        }
    });

    // Submit on Enter.
    searchForm.querySelector('input').addEventListener('keydown', function(e) {
        if (e.key === 'Escape') {
            searchForm.classList.remove('active');
        }
    });
});

// Nav dropdown touch support
document.addEventListener('DOMContentLoaded', function() {
    var dropdownBtn = document.querySelector('.nav-dropdown-btn');
    if (dropdownBtn) {
        dropdownBtn.addEventListener('click', function(e) {
            e.preventDefault();
            this.parentElement.classList.toggle('open');
        });
        document.addEventListener('click', function(e) {
            if (!e.target.closest('.nav-dropdown')) {
                document.querySelector('.nav-dropdown').classList.remove('open');
            }
        });
    }
});
