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

        // Only force captions on when a reviewed track exists. Turning on
        // auto-generated captions would show unverified machine text over a
        // video whose content is dollar amounts and brand names.
        var params = '?autoplay=1&rel=0&modestbranding=1';
        if (facade.getAttribute('data-captions') === '1') {
            params += '&cc_load_policy=1';
        }

        var frame = document.createElement('iframe');
        frame.src = 'https://www.youtube-nocookie.com/embed/' + encodeURIComponent(id) + params;
        frame.title = 'Video walkthrough of the card benefits, credits and fee math';
        frame.allow = 'accelerometer; autoplay; clipboard-write; encrypted-media; picture-in-picture';
        frame.allowFullscreen = true;
        frame.className = 'card-video-frame';

        facade.replaceWith(frame);
        // Keep keyboard focus from being dropped when the button leaves the DOM.
        frame.focus();
    });
})();
