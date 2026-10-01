// Compare page: section toggles and the "Only differences" switch. Without
// JavaScript every section is open and the switch stays hidden.
(function() {
    document.addEventListener('DOMContentLoaded', function() {
        document.querySelectorAll('.compare-section-toggle').forEach(function(btn) {
            btn.addEventListener('click', function() {
                var rows = document.getElementById(btn.getAttribute('aria-controls'));
                var open = btn.getAttribute('aria-expanded') === 'true';
                btn.setAttribute('aria-expanded', open ? 'false' : 'true');
                if (rows) rows.hidden = open;
            });
        });

        // Pin the card header just below the site header, whatever its
        // height at this width (the stylesheet's values are the fallback).
        var page = document.querySelector('.compare-page');
        var nav = document.querySelector('nav.site-nav');
        if (page && nav) {
            var pin = function() { page.style.setProperty('--compare-sticky-top', nav.offsetHeight + 'px'); };
            pin();
            window.addEventListener('resize', pin);
        }

        var table = document.querySelector('.compare-table');
        var diff = document.getElementById('compare-only-diff');
        if (table && diff) {
            diff.closest('label').hidden = false;
            diff.addEventListener('change', function() {
                table.classList.toggle('only-differences', diff.checked);
            });
        }
    });
})();
