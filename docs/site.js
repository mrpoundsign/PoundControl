(() => {
  /* Scrollspy: native scroll-target-group + scrollend aria-current sync,
     with IntersectionObserver fallback for browsers without :target-current */
  const subnav = document.querySelector('.sidebar-subnav');
  if (subnav) {
    const links = Array.from(subnav.querySelectorAll('a[href^="#"]'));
    const targets = links
      .map((link) => {
        const id = link.getAttribute('href').slice(1);
        return { link, target: document.getElementById(id) };
      })
      .filter((entry) => entry.target);

    if (targets.length > 0) {
      if (CSS.supports('scroll-target-group: auto')) {
        const syncAriaCurrent = () => {
          for (const { link } of targets) {
            if (link.matches(':target-current')) {
              link.setAttribute('aria-current', 'true');
            } else {
              link.removeAttribute('aria-current');
            }
          }
        };
        document.addEventListener('scrollend', syncAriaCurrent, { passive: true });
        syncAriaCurrent();
      } else if ('IntersectionObserver' in window) {
        const setActive = (activeLink) => {
          for (const { link } of targets) {
            const isActive = link === activeLink;
            link.classList.toggle('target-current', isActive);
            if (isActive) {
              link.setAttribute('aria-current', 'true');
            } else {
              link.removeAttribute('aria-current');
            }
          }
        };

        const observer = new IntersectionObserver(
          (entries) => {
            const visible = entries
              .filter((entry) => entry.isIntersecting)
              .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
            if (visible.length > 0) {
              const match = targets.find((item) => item.target === visible[0].target);
              if (match) setActive(match.link);
            }
          },
          { rootMargin: '-15% 0px -65% 0px', threshold: 0 }
        );

        for (const { target } of targets) {
          observer.observe(target);
        }
        setActive(targets[0].link);
      }
    }
  }

  /* Copy Button: native CommandEvent (--copy) with click fallback */
  const timers = new WeakMap();
  let lastMessage = '';

  const showCopiedState = (button) => {
    const wrapper = button.closest('.copyable');
    const tip = wrapper ? wrapper.querySelector('.tip') : null;

    button.dataset.copied = 'true';
    button.setAttribute('aria-label', 'Copied to clipboard');

    if (tip) {
      lastMessage = lastMessage === 'Copied!' ? 'Copied!\u00A0' : 'Copied!';
      tip.textContent = lastMessage;
    }

    const existingTimer = timers.get(button);
    if (existingTimer) clearTimeout(existingTimer);

    const timer = setTimeout(() => {
      delete button.dataset.copied;
      button.setAttribute('aria-label', 'Copy code snippet');
      if (tip) tip.textContent = '';
    }, 2000);
    timers.set(button, timer);
  };

  const handleCopy = async (button) => {
    const targetId = button.getAttribute('commandfor');
    if (!targetId) return;
    const source = document.getElementById(targetId);
    if (!source) return;

    const text = source.textContent;
    try {
      await navigator.clipboard.writeText(text);
      showCopiedState(button);
    } catch {
      /* Ignore clipboard denial without crashing */
    }
  };

  if ('CommandEvent' in window) {
    document.addEventListener(
      'command',
      (event) => {
        if (event.command !== '--copy') return;
        const button = event.source;
        if (button instanceof HTMLButtonElement && button.classList.contains('copy-button')) {
          event.preventDefault();
          handleCopy(button);
        }
      },
      { capture: true }
    );
  } else {
    document.addEventListener('click', (event) => {
      if (event.defaultPrevented) return;
      const target = event.target;
      if (!(target instanceof Element)) return;
      const button = target.closest('button.copy-button[command="--copy"]');
      if (button instanceof HTMLButtonElement) {
        event.preventDefault();
        handleCopy(button);
      }
    });
  }
})();
