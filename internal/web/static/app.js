/* ntfy 控制台 · 交互脚本
 * 无外部依赖，全部在 /static 下自托管（满足 CSP script-src 'self'）。
 *
 * 职责：
 *   1. 主题（浅色 / 深色）——在 <head> 同步执行，避免首屏闪烁
 *   2. 移动端抽屉导航
 *   3. 自定义确认弹窗（替代原生 confirm）
 *   4. 提示条关闭与成功提示自动消除
 *   5. server.yml 配置项过滤 / 展开收起
 *   6. 一键复制
 *   7. 防重复提交
 */
(function () {
  "use strict";

  var root = document.documentElement;
  var THEME_KEY = "ntfy-dash-theme";
  var mql = window.matchMedia
    ? window.matchMedia("(prefers-color-scheme: dark)")
    : null;

  /* ---------- 1. 主题 ------------------------------------------------- */

  function stored() {
    try {
      return localStorage.getItem(THEME_KEY);
    } catch (e) {
      return null;
    }
  }

  function resolve(pref) {
    if (pref === "dark" || pref === "light") return pref;
    return mql && mql.matches ? "dark" : "light";
  }

  function applyTheme(pref) {
    var theme = resolve(pref);
    root.setAttribute("data-theme", theme);
    root.style.colorScheme = theme;
    root.setAttribute("data-theme-pref", pref);
    syncThemeButtons(theme, pref);
  }

  function syncThemeButtons(theme, pref) {
    var btns = document.querySelectorAll("[data-theme-toggle]");
    for (var i = 0; i < btns.length; i++) {
      var use = btns[i].querySelector("use");
      if (use) {
        use.setAttribute(
          "href",
          theme === "dark" ? "#i-sun" : "#i-moon"
        );
      }
      btns[i].setAttribute(
        "title",
        theme === "dark" ? "切换到浅色主题" : "切换到深色主题"
      );
      btns[i].setAttribute(
        "aria-label",
        theme === "dark" ? "切换到浅色主题" : "切换到深色主题"
      );
      btns[i].setAttribute("data-theme-pref", pref || "auto");
    }
  }

  // 立即应用：本脚本在 <head> 中同步加载，此时 body 尚未解析，不会闪烁。
  applyTheme(stored() || "auto");

  if (mql && mql.addEventListener) {
    mql.addEventListener("change", function () {
      if ((stored() || "auto") === "auto") applyTheme("auto");
    });
  }

  function toggleTheme() {
    var next = resolve(stored() || "auto") === "dark" ? "light" : "dark";
    try {
      localStorage.setItem(THEME_KEY, next);
    } catch (e) {
      /* 隐私模式下忽略 */
    }
    applyTheme(next);
  }

  /* ---------- 工具 ---------------------------------------------------- */

  function $(sel, ctx) {
    return (ctx || document).querySelector(sel);
  }

  function $$(sel, ctx) {
    return Array.prototype.slice.call((ctx || document).querySelectorAll(sel));
  }

  function on(el, type, fn) {
    if (el) el.addEventListener(type, fn);
  }

  /* ---------- 2. 抽屉导航 --------------------------------------------- */

  var NAV_KEY = "ntfy-dash-nav-open";

  function setNav(open) {
    root.classList.toggle("nav-open", open);
    var btn = $("#navToggle");
    if (btn) btn.setAttribute("aria-expanded", open ? "true" : "false");
    var scrim = $("#scrim");
    if (scrim) scrim.hidden = !open;
    document.body.style.overflow = open ? "hidden" : "";
    if (open) {
      var first = $(".nav-item", $("#sidebar"));
      if (first) first.focus({ preventScroll: true });
    }
  }

  function isNarrow() {
    return window.matchMedia("(max-width: 960px)").matches;
  }

  /* ---------- 3. 确认弹窗 --------------------------------------------- */

  var modalEl = null;
  var modalResolve = null;

  function buildModal() {
    modalEl = document.createElement("div");
    modalEl.className = "modal";
    modalEl.hidden = true;
    modalEl.setAttribute("role", "dialog");
    modalEl.setAttribute("aria-modal", "true");
    modalEl.setAttribute("aria-labelledby", "confirmTitle");
    modalEl.innerHTML =
      '<div class="modal-panel">' +
      '<div class="modal-icon"><svg class="icon"><use href="#i-alert"/></svg></div>' +
      '<h2 id="confirmTitle">确认操作</h2>' +
      '<p class="modal-text"></p>' +
      '<div class="modal-actions">' +
      '<button type="button" class="btn" data-act="cancel">取消</button>' +
      '<button type="button" class="btn btn--danger" data-act="ok">确认</button>' +
      "</div></div>";
    document.body.appendChild(modalEl);

    on(modalEl, "click", function (e) {
      var act = e.target.getAttribute && e.target.getAttribute("data-act");
      if (act) {
        finishModal(act === "ok");
      } else if (e.target === modalEl) {
        finishModal(false);
      }
    });
  }

  function finishModal(ok) {
    if (!modalEl) return;
    modalEl.hidden = true;
    if (modalResolve) {
      var fn = modalResolve;
      modalResolve = null;
      fn(ok);
    }
  }

  function confirmDialog(message, danger) {
    if (!modalEl) buildModal();
    $(".modal-text", modalEl).textContent = message;
    var okBtn = $('[data-act="ok"]', modalEl);
    okBtn.className = danger ? "btn btn--danger" : "btn btn--primary";
    okBtn.textContent = "确认";
    modalEl.hidden = false;
    setTimeout(function () {
      okBtn.focus({ preventScroll: true });
    }, 30);
    return new Promise(function (res) {
      modalResolve = res;
    });
  }

  /* ---------- 4. 提示条 ----------------------------------------------- */

  /* ---------- 8. 图片预览 --------------------------------------------- */

  function initLightbox() {
    var box = $("#lightbox");
    var img = $("#lightboxImg");
    if (!box || !img) return;

    var lastTrigger = null;

    function close() {
      if (box.hidden) return;
      box.hidden = true;
      img.removeAttribute("src");
      document.body.style.overflow = "";
      if (lastTrigger) lastTrigger.focus({ preventScroll: true });
      lastTrigger = null;
    }

    function open(src, trigger) {
      lastTrigger = trigger;
      img.src = src;
      box.hidden = false;
      document.body.style.overflow = "hidden";
      var btn = $("[data-lightbox-close]", box);
      if (btn) btn.focus({ preventScroll: true });
    }

    $$("[data-preview]").forEach(function (trigger) {
      on(trigger, "click", function (e) {
        e.preventDefault();
        var src = trigger.getAttribute("data-preview");
        if (src) open(src, trigger);
      });
    });

    on(box, "click", function (e) {
      // 点背景或关闭按钮都退出，点图片本身不退出
      if (e.target === box || e.target.closest("[data-lightbox-close]")) {
        close();
      }
    });

    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") close();
    });
  }

  function initAlerts() {
    $$(".alert").forEach(function (el) {
      var x = $(".alert-x", el);
      on(x, "click", function () {
        el.remove();
      });
      // 成功提示 8 秒后自动淡出；错误与警告保留，需用户确认阅读
      if (el.classList.contains("alert--ok")) {
        setTimeout(function () {
          if (!el.parentNode) return;
          el.style.transition = "opacity .4s ease, transform .4s ease";
          el.style.opacity = "0";
          el.style.transform = "translateY(-4px)";
          setTimeout(function () {
            el.remove();
          }, 420);
        }, 8000);
      }
    });
  }

  /* ---------- 5. 配置项过滤 ------------------------------------------- */

  function initConfigFilter() {
    var input = $("#cfgFilter");
    var form = $("#cfgForm");
    if (!input || !form) return;

    var rows = $$(".cfg-row", form);
    var groups = $$(".cfg-group", form);
    var counter = $("#cfgCount");
    var onlySet = $("#cfgOnlySet");
    var total = rows.length;

    function apply() {
      var q = input.value.trim().toLowerCase();
      var setOnly = onlySet && onlySet.checked;
      var visible = 0;

      rows.forEach(function (row) {
        var hay = (row.getAttribute("data-search") || "").toLowerCase();
        var ok =
          (!q || hay.indexOf(q) !== -1) &&
          (!setOnly || row.getAttribute("data-set") === "1");
        row.hidden = !ok;
        if (ok) visible++;
      });

      groups.forEach(function (g) {
        var shown = $$(".cfg-row", g).filter(function (r) {
          return !r.hidden;
        }).length;
        g.hidden = shown === 0;
        var badge = $(".cfg-group-badge", g);
        if (badge) badge.textContent = shown + " 项";
        if (q && shown > 0) g.open = true;
      });

      if (counter) {
        counter.textContent =
          q || setOnly ? "显示 " + visible + " / " + total + " 项" : total + " 项配置";
      }
      var none = $("#cfgNone");
      if (none) none.hidden = visible !== 0;
    }

    on(input, "input", apply);
    on(onlySet, "change", apply);
    on($("#cfgClear"), "click", function () {
      input.value = "";
      if (onlySet) onlySet.checked = false;
      apply();
      input.focus();
    });

    on($("#cfgExpand"), "click", function () {
      var open = this.getAttribute("data-open") !== "1";
      groups.forEach(function (g) {
        if (!g.hidden) g.open = open;
      });
      this.setAttribute("data-open", open ? "1" : "0");
      this.textContent = open ? "全部收起" : "全部展开";
    });

    apply();
  }

  /* ---------- 6. 一键复制 --------------------------------------------- */

  function initCopy() {
    $$("[data-copy]").forEach(function (btn) {
      on(btn, "click", function (e) {
        // 按钮可能位于 <summary> 内，需阻止冒泡以免顺带展开／收起面板
        e.preventDefault();
        e.stopPropagation();
        var text = btn.getAttribute("data-copy");
        var done = function () {
          var old = btn.getAttribute("title");
          btn.setAttribute("title", "已复制");
          btn.classList.add("is-copied");
          setTimeout(function () {
            btn.setAttribute("title", old || "复制");
            btn.classList.remove("is-copied");
          }, 1400);
        };
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(done, function () {});
        } else {
          var ta = document.createElement("textarea");
          ta.value = text;
          ta.style.position = "fixed";
          ta.style.opacity = "0";
          document.body.appendChild(ta);
          ta.select();
          try {
            document.execCommand("copy");
            done();
          } catch (e) {
            /* 忽略 */
          }
          ta.remove();
        }
      });
    });
  }

  /* ---------- 7. 表单：确认 + 防重复提交 ------------------------------- */

  function initForms() {
    $$("form").forEach(function (form) {
      var msg = form.getAttribute("data-confirm");

      form.addEventListener("submit", function (e) {
        // 已确认过的表单直接放行（用 dataset 标记，避免二次拦截）
        if (form.dataset.confirmed === "1") return;

        var submitter = form.querySelector('[type="submit"]');

        if (msg) {
          e.preventDefault();
          var danger = !!form.querySelector(".btn--danger");
          confirmDialog(msg, danger).then(function (ok) {
            if (!ok) return;
            form.dataset.confirmed = "1";
            if (form.requestSubmit) {
              form.requestSubmit(submitter || undefined);
            } else {
              form.submit();
            }
          });
          return;
        }

        // 普通表单：提交后短暂禁用按钮，避免重复提交
        if (submitter && !submitter.disabled) {
          setTimeout(function () {
            submitter.disabled = true;
            submitter.setAttribute("aria-busy", "true");
            setTimeout(function () {
              submitter.disabled = false;
              submitter.removeAttribute("aria-busy");
            }, 6000);
          }, 0);
        }
      });
    });
  }

  /* ---------- 启动 ---------------------------------------------------- */

  function init() {
    on($("#navToggle"), "click", function () {
      setNav(!root.classList.contains("nav-open"));
    });
    on($("#scrim"), "click", function () {
      setNav(false);
    });
    $$("[data-theme-toggle]").forEach(function (btn) {
      on(btn, "click", toggleTheme);
    });
    $$("[data-back]").forEach(function (btn) {
      on(btn, "click", function () {
        if (window.history.length > 1) window.history.back();
        else window.location.href = "/";
      });
    });
    $$(".nav-item").forEach(function (a) {
      on(a, "click", function () {
        if (isNarrow()) setNav(false);
      });
    });

    document.addEventListener("keydown", function (e) {
      if (e.key !== "Escape") return;
      if (modalEl && !modalEl.hidden) {
        finishModal(false);
        return;
      }
      if (root.classList.contains("nav-open")) setNav(false);
    });

    window.addEventListener("resize", function () {
      if (!isNarrow() && root.classList.contains("nav-open")) setNav(false);
    });

    initAlerts();
    initConfigFilter();
    initCopy();
    initForms();
    initLightbox();
    syncThemeButtons(resolve(stored() || "auto"), stored() || "auto");
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
