// Image picker shared by the reply composer and the Dispatch screen: an
// attach button and thumbnails with remove buttons. Previews are data: URLs
// (the CSP allows no blob: images).
import { checkAttachment } from "./logic.js";
import { clear, h, toast } from "./ui.js";

/**
 * imagePicker returns {controls, count, files, clear}: controls are the
 * elements to place in a form, files() the picked File objects.
 */
export function imagePicker() {
  let files = [];
  const thumbs = h("div", { class: "thumbs", hidden: true });
  const input = h("input", {
    type: "file",
    accept: "image/*",
    multiple: "",
    class: "file-input",
    hidden: true,
    onchange: () => {
      for (const f of input.files) {
        const why = checkAttachment(f, files.length);
        if (why) {
          toast(why, "error");
          continue;
        }
        files.push(f);
      }
      input.value = "";
      draw();
    },
  });
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
  const button = h("button", { type: "button", class: "btn small attach", onclick: () => input.click() }, "Attach image");
  return {
    controls: [thumbs, h("div", { class: "row" }, button, input)],
    count: () => files.length,
    files: () => [...files],
    clear: () => {
      files = [];
      draw();
    },
  };
}
