// Image picker shared by the reply composer and the Dispatch screen: an
// attach button and thumbnails with remove buttons. Previews are data: URLs
// (the CSP allows no blob: images).
import { checkAttachment, imageExt, pasteHasText, pastedImages } from "./logic.js";
import { clear, h, toast } from "./ui.js";

export const CAMERA_SVG =
  '<svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 8h3l1.5-2h7L17 8h3a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1z"/><circle cx="12" cy="13" r="3.5"/></svg>';

export const PASTE_SVG =
  '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="5" y="4" width="14" height="17" rx="2"/><path d="M9 4h6v3H9z"/><path d="M9 13h6M9 17h4"/></svg>';

/** svgIcon builds an element from a trusted, static SVG string. */
export function svgIcon(markup) {
  const t = document.createElement("template");
  t.innerHTML = markup;
  return t.content.firstElementChild;
}

/**
 * imagePicker returns {controls, trigger, count, files, clear}: controls are
 * the elements to place in a form, files() the picked File objects. With
 * {icon: true} the controls are only the thumbnails, and trigger is the
 * camera icon button (plus its hidden input) for the caller to place; the
 * default puts a text "Attach image" button in controls.
 */
export function imagePicker({ icon = false } = {}) {
  let files = [];
  const thumbs = h("div", { class: "thumbs", hidden: true });
  const input = h("input", {
    type: "file",
    accept: "image/*",
    multiple: "",
    class: "file-input",
    hidden: true,
    onchange: () => {
      addFiles(input.files);
      input.value = "";
    },
  });
  // addFiles validates and adds each file, toasting rejects, then redraws.
  function addFiles(list) {
    for (const f of list) {
      const why = checkAttachment(f, files.length);
      if (why) {
        toast(why, "error");
        continue;
      }
      files.push(f);
    }
    draw();
  }
  // bindPaste attaches images pasted into a textarea. A paste with text still
  // inserts the text; only an image-only paste is intercepted.
  function bindPaste(textarea) {
    textarea.addEventListener("paste", (e) => {
      const imgs = pastedImages(e.clipboardData);
      if (!imgs.length) return;
      if (!pasteHasText(e.clipboardData)) e.preventDefault();
      addFiles(imgs);
    });
  }
  // pasteFromClipboard is the button fallback for browsers (iOS Safari) that
  // offer no Paste in a textarea for an image-only clipboard. It must run
  // directly in the click handler so the user gesture is intact.
  async function pasteFromClipboard() {
    const picked = [];
    try {
      for (const item of await navigator.clipboard.read()) {
        const type = item.types.find((t) => /^image\/(png|jpe?g|gif|webp)$/i.test(t)) ?? item.types.find((t) => /^image\//i.test(t));
        if (!type) continue;
        const blob = await item.getType(type);
        picked.push(new File([blob], `pasted.${imageExt(type)}`, { type }));
      }
    } catch {
      // NotAllowedError and friends fall through to the toast below.
    }
    if (!picked.length) {
      toast("No image on the clipboard", "error");
      return;
    }
    addFiles(picked);
  }
  function draw() {
    clear(thumbs);
    thumbs.hidden = files.length === 0;
    for (const f of files) {
      const img = h("img", { alt: "Attached image" });
      const reader = new FileReader();
      reader.onload = () => {
        img.src = reader.result;
      };
      reader.readAsDataURL(f);
      thumbs.append(
        h(
          "div",
          { class: "thumb" },
          img,
          h(
            "button",
            {
              type: "button",
              class: "thumb-remove",
              "aria-label": "Remove image",
              onclick: () => {
                files = files.filter((x) => x !== f);
                draw();
              },
            },
            "×",
          ),
        ),
      );
    }
  }
  const button = icon
    ? h("button", { type: "button", class: "icon-btn attach", "aria-label": "Attach image", onclick: () => input.click() }, svgIcon(CAMERA_SVG))
    : h("button", { type: "button", class: "btn small attach", onclick: () => input.click() }, "Attach image");
  const canPaste = typeof navigator !== "undefined" && typeof navigator.clipboard?.read === "function";
  const pasteBtn = !canPaste
    ? null
    : icon
      ? h("button", { type: "button", class: "icon-btn paste", "aria-label": "Paste image", onclick: pasteFromClipboard }, svgIcon(PASTE_SVG))
      : h("button", { type: "button", class: "btn small paste", onclick: pasteFromClipboard }, "Paste image");
  return {
    controls: icon ? [thumbs] : [thumbs, h("div", { class: "row" }, button, pasteBtn, input)],
    trigger: icon ? [button, pasteBtn, input].filter(Boolean) : [],
    addFiles,
    bindPaste,
    count: () => files.length,
    files: () => [...files],
    clear: () => {
      files = [];
      draw();
    },
  };
}
