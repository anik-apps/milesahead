// Click-to-load YouTube facade.
//
// The page ships a poster image and a play button; the YouTube iframe is only
// injected on click, so no third-party request is made until the visitor opts in.
(function () {
    var facade = document.querySelector('.card-video-facade');
    if (!facade) return;

    facade.addEventListener('click', function () {
        var id = facade.getAttribute('data-video-id');
        if (!id) return;

        var frame = document.createElement('iframe');
        frame.src = 'https://www.youtube-nocookie.com/embed/' + encodeURIComponent(id) +
            '?autoplay=1&rel=0&modestbranding=1&cc_load_policy=1';
        frame.title = 'Video walkthrough of the card benefits, credits and fee math';
        frame.allow = 'accelerometer; autoplay; clipboard-write; encrypted-media; picture-in-picture';
        frame.allowFullscreen = true;
        frame.className = 'card-video-frame';

        facade.replaceWith(frame);
        // Keep keyboard focus from being dropped when the button leaves the DOM.
        frame.focus();
    });
})();
