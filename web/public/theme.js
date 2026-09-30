// Applied before first paint. Dark is the default and stamps nothing;
// "light" and "system" are stamped on <html>.
try {
  var t = localStorage.getItem("bellwether.theme");
  if (t === "light" || t === "system") document.documentElement.setAttribute("data-theme", t);
} catch (e) {}
