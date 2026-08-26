(function () {
    "use strict";

    var invalidLinkMessage = "Link must be an absolute HTTP or HTTPS URL without credentials.";

    function showError(form, message) {
        var output = form.querySelector("[data-form-error]");
        if (!output) {
            return;
        }
        output.textContent = message;
        output.hidden = !message;
    }

    function linkError(input, isRequired) {
        var value = input.value.trim();
        if (!value) {
            return isRequired ? "Enter a link URL." : "";
        }
        if (/\\|\s/u.test(value)) {
            return invalidLinkMessage;
        }
        try {
            var parsed = new URL(value);
            if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.username || parsed.password) {
                return invalidLinkMessage;
            }
        } catch (_) {
            return invalidLinkMessage;
        }
        return "";
    }

    function validateLinks(form, requireLink) {
        var rows = form.querySelectorAll("[data-link-row]");
        var hasLink = false;
        for (var i = 0; i < rows.length; i += 1) {
            var label = rows[i].querySelector('[name="link_label"]');
            var url = rows[i].querySelector('[name="link_url"]');
            var hasValue = label.value.trim() !== "" || url.value.trim() !== "";
            hasLink = hasLink || hasValue;
            url.setCustomValidity("");
            var message = linkError(url, requireLink || hasValue);
            if (message) {
                url.setCustomValidity(message);
                return {input: url, message: message};
            }
        }
        if (requireLink && !hasLink) {
            return {input: rows[0].querySelector('[name="link_url"]'), message: "Add at least one link."};
        }
        return null;
    }

    function tagsError(input) {
        var keys = new Set();
        var lines = input.value.split(/\r?\n/u);
        for (var i = 0; i < lines.length; i += 1) {
            var line = lines[i].trim();
            if (!line) {
                continue;
            }
            var separator = line.indexOf("=");
            var key = separator < 0 ? "" : line.slice(0, separator).trim();
            if (!key) {
                return "Tags must use one key=value pair per line.";
            }
            if (keys.has(key)) {
                return "Each tag key may appear only once.";
            }
            keys.add(key);
        }
        return "";
    }

    function reject(form, failure) {
        showError(form, failure.message);
        failure.input.setCustomValidity(failure.message);
        failure.input.reportValidity();
    }

    function resetValidationOnInput(form) {
        form.addEventListener("input", function (event) {
            if (typeof event.target.setCustomValidity === "function") {
                event.target.setCustomValidity("");
            }
            showError(form, "");
        });
    }

    function prepareRecordForm(form) {
        var tags = form.querySelector('[name="tags"]');
        resetValidationOnInput(form);
        form.addEventListener("submit", function (event) {
            tags.setCustomValidity("");
            var message = tagsError(tags);
            if (message) {
                event.preventDefault();
                reject(form, {input: tags, message: message});
                return;
            }
            var failure = validateLinks(form, false);
            if (failure) {
                event.preventDefault();
                reject(form, failure);
            }
        });
    }

    function prepareAddLinksForm(form) {
        var rows = form.querySelector("#link-rows");
        var template = form.querySelector("#link-row-template");
        var addButton = form.querySelector("#add-link-row");
        var nextRowID = rows.querySelectorAll("[data-link-row]").length;

        resetValidationOnInput(form);

        addButton.addEventListener("click", function () {
            var fragment = template.content.cloneNode(true);
            var row = fragment.querySelector(".link-row");
            var inputs = row.querySelectorAll("input");
            var labels = row.querySelectorAll("label");
            inputs[0].id = "link-label-" + nextRowID;
            inputs[1].id = "link-url-" + nextRowID;
            labels[0].htmlFor = inputs[0].id;
            labels[1].htmlFor = inputs[1].id;
            nextRowID += 1;
            rows.appendChild(fragment);
        });
        rows.addEventListener("click", function (event) {
            if (event.target.classList.contains("remove-link-row")) {
                event.target.closest(".link-row").remove();
                showError(form, "");
            }
        });
        form.addEventListener("submit", function (event) {
            var failure = validateLinks(form, true);
            if (failure) {
                event.preventDefault();
                reject(form, failure);
            }
        });
    }

    var recordForm = document.querySelector("[data-record-form]");
    if (recordForm) {
        prepareRecordForm(recordForm);
    }
    var addLinksForm = document.querySelector("[data-add-links-form]");
    if (addLinksForm) {
        prepareAddLinksForm(addLinksForm);
    }
}());
