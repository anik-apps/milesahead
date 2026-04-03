// Compare Page — dropdown navigation and section collapse
(function() {
    document.addEventListener('DOMContentLoaded', function() {
        // Selector form submit: build /compare?cards=slug1,slug2,slug3
        var form = document.getElementById('compare-selector-form');
        if (form) {
            form.addEventListener('submit', function(e) {
                e.preventDefault();
                var selects = form.querySelectorAll('select');
                var slugs = [];
                selects.forEach(function(sel) {
                    if (sel.value) slugs.push(sel.value);
                });
                if (slugs.length >= 2) {
                    window.location.href = '/compare?cards=' + slugs.join(',');
                }
            });
        }

        // Header dropdown change: rebuild URL and navigate
        var headerSelects = document.querySelectorAll('.compare-header-select');
        if (headerSelects.length) {
            headerSelects.forEach(function(sel) {
                sel.addEventListener('change', function() {
                    var allSelects = document.querySelectorAll('.compare-header-select');
                    var slugs = [];
                    allSelects.forEach(function(s) {
                        if (s.value) slugs.push(s.value);
                    });
                    if (slugs.length >= 2) {
                        window.location.href = '/compare?cards=' + slugs.join(',');
                    }
                });
            });

            // Set the correct selected option for each header select
            headerSelects.forEach(function(sel) {
                var currentSlug = sel.getAttribute('data-slot');
                var options = sel.querySelectorAll('option');
                options.forEach(function(opt) {
                    opt.selected = (opt.value === currentSlug);
                });
            });
        }

        // Section collapse toggle
        var sectionHeaders = document.querySelectorAll('.compare-section-header');
        sectionHeaders.forEach(function(header) {
            header.style.cursor = 'pointer';
            header.addEventListener('click', function() {
                var section = header.getAttribute('data-section');
                var rows = document.querySelectorAll('.section-' + section);
                var isCollapsed = header.classList.contains('collapsed');

                if (isCollapsed) {
                    header.classList.remove('collapsed');
                    header.querySelector('.compare-toggle-icon').innerHTML = '&#9660;';
                    rows.forEach(function(r) { r.style.display = ''; });
                } else {
                    header.classList.add('collapsed');
                    header.querySelector('.compare-toggle-icon').innerHTML = '&#9654;';
                    rows.forEach(function(r) { r.style.display = 'none'; });
                }
            });
        });
    });
})();
