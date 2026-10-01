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

// Open all article content links in new tabs
document.addEventListener('DOMContentLoaded', function() {
    document.querySelectorAll('.article-content a').forEach(function(link) {
        link.setAttribute('target', '_blank');
        link.setAttribute('rel', 'noopener');
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
