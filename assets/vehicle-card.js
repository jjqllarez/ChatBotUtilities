class VehicleCardPro extends HTMLElement {
      static get observedAttributes() {
        return ['marca', 'modelo', 'version', 'tipo', 'motorizacion', 'precio', 'moneda', 'imagen'];
      }

      constructor() {
        super();
        this.attachShadow({ mode: 'open' });
      }

      attributeChangedCallback() { this.render(); }
      connectedCallback()       { this.render(); }

      get(attr, fallback = 'â€”') {
        return this.getAttribute(attr) || fallback;
      }

      render() {
        const data = {
          marca:        this.get('marca'),
          modelo:       this.get('modelo'),
          version:      this.get('version'),
          tipo:         this.get('tipo'),
          motorizacion: this.get('motorizacion'),
          precio:       this.get('precio'),
          moneda:       this.get('moneda', 'USD'),
          imagen:       this.get('imagen', '')
        };

        this.shadowRoot.innerHTML = `
          <style>
            :host { display: inline-block; }
            * { box-sizing: border-box; margin: 0; }

            .card {
              width: 360px;
              max-width: 92vw;
              background: #18181b;
              border: 1px solid #27272a;
              border-radius: 12px;
              overflow: hidden;
              transition: transform .25s ease, box-shadow .25s ease;
              position: relative;
              font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif;
            }
            .card:hover {
              transform: translateY(-4px);
              box-shadow: 0 16px 40px -8px rgba(0,0,0,.45);
            }

            /* ---------- ZONA DE IMAGEN ---------- */
            .media {
              position: relative;
              height: 220px;
              overflow: hidden;
              background: #0f0f11;
            }
            .media img {
              width: 100%;
              height: 100%;
              object-fit: cover;
              transition: transform .5s ease;
              display: block;
            }
            .card:hover .media img { transform: scale(1.05); }
            .media::after {
              content: "";
              position: absolute;
              inset: 0;
              background: linear-gradient(to top, rgba(0,0,0,.65) 0%, transparent 60%);
            }

            .badge {
              position: absolute;
              z-index: 2;
              padding: 5px 12px;
              border-radius: 999px;
              font-size: 11px;
              font-weight: 500;
              letter-spacing: .6px;
              text-transform: uppercase;
            }
            .badge.marca {
              top: 12px; left: 12px;
              background: rgba(220, 38, 38, .92);
              color: #fff;
            }
            .badge.tipo {
              top: 12px; right: 12px;
              background: rgba(255,255,255,.10);
              color: #fff;
              border: 1px solid rgba(255,255,255,.18);
              backdrop-filter: blur(4px);
            }

            /* ---------- CUERPO ---------- */
            .body { padding: 20px 20px 16px; }

            .modelo {
              font-size: 26px;
              font-weight: 500;
              color: #fafafa;
              line-height: 1.15;
            }
            .version {
              margin-top: 6px;
              font-size: 13px;
              font-weight: 400;
              color: #a1a1aa;
              letter-spacing: .3px;
            }

            .specs {
              display: flex;
              gap: 8px;
              flex-wrap: wrap;
              margin-top: 16px;
            }
            .chip {
              display: inline-flex;
              align-items: center;
              gap: 6px;
              padding: 6px 12px;
              background: #27272a;
              border: 1px solid #3f3f46;
              border-radius: 6px;
              font-size: 12px;
              font-weight: 400;
              color: #d4d4d8;
            }
            .chip svg { width: 14px; height: 14px; color: #71717a; flex-shrink: 0; }

            /* ---------- PIE CON PRECIO ---------- */
            .footer {
              display: flex;
              align-items: center;
              justify-content: space-between;
              gap: 12px;
              padding: 16px 20px;
              background: #0f0f11;
              border-top: 1px solid #27272a;
            }
            .price-label {
              font-size: 11px;
              font-weight: 500;
              text-transform: uppercase;
              letter-spacing: 1.2px;
              color: #52525b;
            }
            .price {
              font-size: 22px;
              font-weight: 500;
              color: #fafafa;
              font-variant-numeric: tabular-nums;
              font-feature-settings: "tnum" 1;
            }
            .price span { color: #f87171; font-size: 13px; font-weight: 500; margin-right: 4px; }

            .cta {
              border: none;
              cursor: pointer;
              padding: 10px 18px;
              border-radius: 8px;
              font-size: 13px;
              font-weight: 500;
              color: #fff;
              background: #dc2626;
              transition: filter .15s ease, transform .15s ease;
            }
            .cta:hover  { filter: brightness(1.12); transform: scale(1.03); }
            .cta:active { transform: scale(.97); }
          </style>

          <article class="card">
            <div class="media">
              <img src="${data.imagen}" alt="${data.marca} ${data.modelo}"
                   onerror="this.style.display='none'">
              <span class="badge marca">${data.marca}</span>
              <span class="badge tipo">${data.tipo}</span>
            </div>

            <div class="body">
              <h2 class="modelo">${data.modelo}</h2>
              <p class="version">${data.version}</p>

              <div class="specs">
                <span class="chip">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M3 13l2-5a2 2 0 0 1 1.9-1.3h10.2A2 2 0 0 1 19 8l2 5"/>
                    <path d="M3 13h18v4h-2"/><path d="M3 17h2"/>
                    <circle cx="7.5" cy="17.5" r="1.8"/><circle cx="16.5" cy="17.5" r="1.8"/>
                  </svg>
                  ${data.tipo}
                </span>
                <span class="chip">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M4 21V6a2 2 0 0 1 2-2h6a2 2 0 0 1 2 2v15"/>
                    <path d="M3 21h12"/>
                    <path d="M14 10h2a2 2 0 0 1 2 2v4.5a1.5 1.5 0 0 0 3 0V9.8a2 2 0 0 0-.6-1.4L18 6"/>
                    <path d="M7 8h4"/>
                  </svg>
                  ${data.motorizacion}
                </span>
              </div>
            </div>

            <div class="footer">
              <div>
                <div class="price-label">Precio</div>
                <div class="price"><span>${data.moneda}</span> ${data.precio}</div>
              </div>
              <button class="cta">Consultar</button>
            </div>
          </article>
        `;

        // Evento personalizado al hacer clic en "Consultar"
        const btn = this.shadowRoot.querySelector('.cta');
        btn?.addEventListener('click', () => {
          this.dispatchEvent(new CustomEvent('vehicle-consult', {
            bubbles: true, composed: true, detail: data
          }));
        });
      }
    }

    customElements.define('vehicle-card-pro', VehicleCardPro);

    // Ejemplo: escuchar el evento del botÃ³n
    document.addEventListener('vehicle-consult', e => {
      console.log('Consulta por:', e.detail.marca, e.detail.modelo, e.detail.version);
    });