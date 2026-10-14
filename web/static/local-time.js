(function () {
    "use strict";

    // Omitting timeZone uses the viewer's timezone, including daylight saving.
    var timeFormatter = new Intl.DateTimeFormat(undefined, {
        hourCycle: "h23",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        timeZoneName: "short"
    });
    var dateFormatter = new Intl.DateTimeFormat(undefined, {
        year: "numeric",
        month: "2-digit",
        day: "2-digit"
    });

    function renderTimes() {
        var today = new Date();
        document.querySelectorAll("time[data-local-time]").forEach(function (element) {
            var date = new Date(element.getAttribute("datetime"));
            if (!Number.isNaN(date.getTime())) {
                var isToday = date.getFullYear() === today.getFullYear() &&
                    date.getMonth() === today.getMonth() && date.getDate() === today.getDate();
                element.textContent = timeFormatter.format(date) +
                    (isToday ? "" : " · " + dateFormatter.format(date));
            }
        });
    }

    renderTimes();
    // Keep the date accurate when a page stays open across local midnight.
    setInterval(renderTimes, 60 * 1000);
    document.addEventListener("visibilitychange", function () {
        if (!document.hidden) {
            renderTimes();
        }
    });
}());
