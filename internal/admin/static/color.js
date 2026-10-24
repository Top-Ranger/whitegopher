// Progressive enhancement: pair each colour picker with its hex text field.
// The text field stays the source of truth (it may be empty or carry alpha).
document.querySelectorAll('.colorfield').forEach(function (f) {
  var pick = f.querySelector('input[type=color]');
  var text = f.querySelector('input[type=text]');
  var hex = /^#[0-9a-fA-F]{6}/;
  function sync() {
    var m = hex.exec(text.value);
    if (m) pick.value = m[0].toLowerCase();
  }
  pick.addEventListener('input', function () {
    var alpha = /^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})$/.exec(text.value);
    text.value = pick.value + (alpha ? alpha[1] : '');
  });
  text.addEventListener('input', sync);
  sync();
  f.classList.add('enhanced');
});
