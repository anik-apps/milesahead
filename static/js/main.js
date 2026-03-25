// Theme Toggle
(function() {
    const html = document.documentElement;
    const saved = localStorage.getItem('theme');
    if (saved) {
        html.setAttribute('data-theme', saved);
    }

    document.addEventListener('DOMContentLoaded', function() {
        const toggleBtn = document.getElementById('theme-toggle');
        if (toggleBtn) {
            toggleBtn.addEventListener('click', function() {
                const current = html.getAttribute('data-theme');
                const next = current === 'dark' ? 'light' : 'dark';
                html.setAttribute('data-theme', next);
                localStorage.setItem('theme', next);
            });
        }
    });
})();

// Vote Handler
document.addEventListener('DOMContentLoaded', function() {
    var voteSection = document.querySelector('.vote-section');
    if (!voteSection) return;

    var articleId = voteSection.getAttribute('data-article-id');
    var votedKey = 'voted_' + articleId;

    // Check if already voted.
    if (localStorage.getItem(votedKey)) {
        var btns = voteSection.querySelectorAll('.vote-btn');
        btns.forEach(function(btn) { btn.disabled = true; });
        return;
    }

    voteSection.addEventListener('click', function(e) {
        var btn = e.target.closest('.vote-btn');
        if (!btn || btn.disabled) return;

        var vote = parseInt(btn.getAttribute('data-vote'), 10);

        fetch('/api/vote', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ article_id: parseInt(articleId, 10), vote: vote })
        })
        .then(function(res) { return res.json(); })
        .then(function(data) {
            if (data.status === 'ok') {
                var upEl = voteSection.querySelector('.upvotes');
                var downEl = voteSection.querySelector('.downvotes');
                if (upEl) upEl.textContent = data.upvotes;
                if (downEl) downEl.textContent = data.downvotes;

                btn.classList.add('active');
                var btns = voteSection.querySelectorAll('.vote-btn');
                btns.forEach(function(b) { b.disabled = true; });

                localStorage.setItem(votedKey, '1');
            }
        })
        .catch(function(err) {
            console.error('Vote failed:', err);
        });
    });
});

// Category Filter - highlight active tab based on URL
document.addEventListener('DOMContentLoaded', function() {
    var tabs = document.querySelectorAll('.category-tabs .tab');
    tabs.forEach(function(tab) {
        tab.addEventListener('click', function(e) {
            // Let the default link behavior handle navigation.
            // The server re-renders with the active class based on query param.
        });
    });
});
