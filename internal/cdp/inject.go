// Package cdp implements the Chrome DevTools Protocol side: target
// discovery, per-window sessions, and the page-side JS injection.
package cdp

// BindingName is the CDP runtime binding global. It must differ from the
// Python POC's binding so the two tools can coexist, but only ONE
// instance of this tool should run at a time (bindings are page globals;
// a second instance would override the first one's binding).
const BindingName = "vscodeLoadLlama"

// extractFn is an arrow function (NOT invoked) that reads the current
// chat input state. It must stay a function: InjectJS assigns it to a
// variable and calls it.
const extractFn = `() => {
    const q = sel => document.querySelector(sel);
    const vis = el => el && !!(el.offsetWidth || el.offsetHeight);

    const inputEditor = q('.chat-input-container .interactive-input-editor .monaco-editor')
                     || q('.interactive-input-editor .monaco-editor');
    const inputText = inputEditor ? (inputEditor.innerText || '').replace(/\n+$/, '') : null;

    const modelEl = q('a.model-picker-name');
    const model = modelEl ? (modelEl.getAttribute('aria-label') || '').replace(/^Models,\s*/, '')
                          : (modelEl ? modelEl.textContent.trim() : null);
    const effortEl = q('.model-picker-config');
    const effort = effortEl ? (effortEl.getAttribute('aria-label') || '').replace(/^Reasoning Effort:\s*/i, '')
                            : (effortEl ? effortEl.textContent.trim() : null);
    const modeEl = q('.chat-input-picker-item .chat-input-picker-label');
    const mode = modeEl ? modeEl.textContent.trim() : null;

    return {
      input: inputText,
      model: model,
      effort: effort,
      mode: mode,
      inputVisible: vis(inputEditor),
      modelVisible: vis(modelEl)
    };
  }`

// InjectJS installs a versioned MutationObserver that debounces (50ms)
// DOM changes in the whole document and pushes the state through
// window.<BindingName>. It always emits the current state at the end,
// so (re)connecting or reloading yields an immediate snapshot.
const InjectJS = `(() => {
  const extract = ` + extractFn + `
  ;
  const push = () => {
    try { window.` + BindingName + `(JSON.stringify(extract())); } catch (e) {}
  };

  // versioned install: replaces any stale observer from older versions
  const VERSION = 1;
  if (window.__loadLlamaVersion !== VERSION) {
    if (window.__loadLlamaMO) { try { window.__loadLlamaMO.disconnect(); } catch (e) {} }
    window.__loadLlamaVersion = VERSION;
    let timer = null;
    window.__loadLlamaSchedule = () => {
      if (timer) return;
      timer = setTimeout(() => { timer = null; push(); }, 50);
    };
    window.__loadLlamaMO = new MutationObserver(window.__loadLlamaSchedule);
    window.__loadLlamaMO.observe(document.documentElement, {
      subtree: true, childList: true, characterData: true,
      attributes: true, attributeFilter: ['aria-label']
    });
  }

  // always emit current state (re-attach / re-inject after reload)
  (window.__loadLlamaSchedule || push)();
  return 'ok';
})()`
