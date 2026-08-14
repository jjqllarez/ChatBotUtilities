// ============================================================
// WEB COMPONENT <capital-motors-cotizacion> (compartido)
// Usado por: nueva-cotizacion.astro y listar.astro (búsqueda de
// cotizaciones emitidas). Se registra una sola vez.
// ============================================================
class CapitalMotorsCotizacion extends HTMLElement {
  static get observedAttributes() {
    return [
      'cliente-nombre', 'cliente-ci', 'cliente-email', 'cliente-telefono', 'cliente-vendedor',
      'socio-comercial', // Datos de la empresa
      'logo', 'numero-presupuesto', 'forma-pago', 'fecha-emision',
      'imagen-auto', 'datos-vehiculo', 'bloques', 'condiciones'
    ];
  }

  constructor() {
    super();
    this.attachShadow({ mode: 'open' });
    this._cliente = { nombre: '', ci: '', email: '', telefono: '', vendedor: '' };
    this._socioComercial = { razon_social: '', rif: '', telefono: '', correo: '', direccion: '', logo_url: '' };
    this._logo = '';
    this._numeroPresupuesto = '';
    this._formaPago = '';
    this._fechaEmision = new Date().toLocaleDateString('es-ES');
    this._imagenAuto = '';
    this._datosVehiculo = {};
    this._bloques = [];
    this._condiciones = '';
  }

  set clienteNombre(val) { this.setAttribute('cliente-nombre', val); }
  set clienteCi(val) { this.setAttribute('cliente-ci', val); }
  set clienteEmail(val) { this.setAttribute('cliente-email', val); }
  set clienteTelefono(val) { this.setAttribute('cliente-telefono', val); }
  set clienteVendedor(val) { this.setAttribute('cliente-vendedor', val); }
  set logo(val) { this.setAttribute('logo', val); }
  set numeroPresupuesto(val) { this.setAttribute('numero-presupuesto', val); }
  set formaPago(val) { this.setAttribute('forma-pago', val); }
  set fechaEmision(val) { this.setAttribute('fecha-emision', val); }
  set imagenAuto(val) { this.setAttribute('imagen-auto', val); }
  set condiciones(val) { this.setAttribute('condiciones', val); }

  set socioComercial(val) {
    if (typeof val === 'string') {
      try { val = JSON.parse(val); } catch(e) { val = {}; }
    }
    this._socioComercial = val || {};
    this.setAttribute('socio-comercial', JSON.stringify(this._socioComercial));
  }

  set datosVehiculo(val) {
    if (typeof val === 'string') {
      try { val = JSON.parse(val); } catch(e) { val = {}; }
    }
    this._datosVehiculo = val;
    this.setAttribute('datos-vehiculo', JSON.stringify(val));
  }

  set bloques(val) {
    if (typeof val === 'string') {
      try { val = JSON.parse(val); } catch(e) { val = []; }
    }
    this._bloques = Array.isArray(val) ? val : [];
    this.setAttribute('bloques', JSON.stringify(this._bloques));
  }

  attributeChangedCallback(name, oldValue, newValue) {
    switch (name) {
      case 'cliente-nombre': this._cliente.nombre = newValue || ''; break;
      case 'cliente-ci': this._cliente.ci = newValue || ''; break;
      case 'cliente-email': this._cliente.email = newValue || ''; break;
      case 'cliente-telefono': this._cliente.telefono = newValue || ''; break;
      case 'cliente-vendedor': this._cliente.vendedor = newValue || ''; break;
      case 'logo': this._logo = newValue || ''; break;
      case 'numero-presupuesto': this._numeroPresupuesto = newValue || ''; break;
      case 'forma-pago': this._formaPago = newValue || ''; break;
      case 'fecha-emision': this._fechaEmision = newValue || new Date().toLocaleDateString('es-ES'); break;
      case 'imagen-auto': this._imagenAuto = newValue || ''; break;
      case 'condiciones': this._condiciones = newValue || ''; break;
      case 'socio-comercial':
        try { this._socioComercial = JSON.parse(newValue) || {}; } catch(e) { this._socioComercial = {}; }
        break;
      case 'datos-vehiculo':
        try { this._datosVehiculo = JSON.parse(newValue); } catch(e) { this._datosVehiculo = {}; }
        break;
      case 'bloques':
        try { this._bloques = JSON.parse(newValue); } catch(e) { this._bloques = []; }
        if (!Array.isArray(this._bloques)) this._bloques = [];
        break;
    }
    this.render();
  }

  _formatDireccion(direccion, maxChars = 55) {
    if (!direccion) return '';
    const palabras = String(direccion).split(' ');
    let lineas = [];
    let lineaActual = '';
    for (const palabra of palabras) {
      if ((lineaActual + ' ' + palabra).trim().length > maxChars) {
        if (lineaActual) lineas.push(lineaActual.trim());
        lineaActual = palabra;
      } else {
        lineaActual = (lineaActual + ' ' + palabra).trim();
      }
    }
    if (lineaActual) lineas.push(lineaActual.trim());
    return lineas.join('<br>');
  }

  connectedCallback() {
    this.render();
    setTimeout(() => { this._ajustarEscala(); this._ajustarEscalaAncho(); }, 50);
  }

  render() {
    if (!this.shadowRoot) return;

    let vehiculoHTML = '';
    const datos = this._datosVehiculo;
    if (datos && typeof datos === 'object' && Object.keys(datos).length > 0) {
      for (const [key, value] of Object.entries(datos)) {
        vehiculoHTML += `<div><strong>${key}:</strong> ${value}</div>`;
      }
    } else {
      vehiculoHTML = '<div>No hay datos del vehículo</div>';
    }

    let bloquesHTML = '';
    if (this._bloques && this._bloques.length > 0) {
      for (const bloque of this._bloques) {
        // El punto final del título es el marcador interno del "bloque resumen":
        // no se imprime en la hoja (p.ej. "Datos." se muestra como "Datos")
        const nombre = (bloque.nombre || 'Bloque').replace(/\.+$/g, '').trim() || 'Bloque';
        const lineas = Array.isArray(bloque.lineas) ? bloque.lineas : [];
        let lineasHTML = lineas.length > 0 
          ? lineas.map(linea => `<div>${linea}</div>`).join('')
          : '<div>Sin información</div>';
        bloquesHTML += `
          <div class="section-header">${nombre}</div>
          <div class="info-box">${lineasHTML}</div>
        `;
      }
    }

    const styles = `
      * { margin:0; padding:0; box-sizing:border-box; }
      :host { display: block; font-family: 'Arial', sans-serif; font-size: 12px; background: white; color: #000; line-height: 1.25; }
      .page { position: relative; width: 21.59cm; height: 27.94cm; margin: 0 auto; padding: 1cm; background: white; overflow: hidden; transition: transform 0.1s ease; }
      .header-container { display: flex; align-items: center; justify-content: space-between; padding-bottom: 6px; margin-bottom: 6px; }
      .logo-box { width: 160px; height: 80px; display: flex; align-items: center; justify-content: center; }
      .logo-box img { max-width: 100%; max-height: 100%; object-fit: contain; }
      .header-info { text-align: center; flex: 1; min-width: 0; padding: 0 5px; overflow: hidden; }
      .header-info h2 { font-size: 15px; margin: 0 0 2px 0; font-weight: bold; }
      .header-info p { font-size: 8px; margin: 1px auto; text-transform: uppercase; max-width: 440px; word-break: break-word; line-height: 1.3; }
      .header-right { width: 170px; text-align: right; font-size: 10px; font-weight: bold; }
      .section-header { background-color: #d1d1d1; padding: 3px 6px; margin: 8px 0 5px 0; font-weight: bold; border: 1px solid #999; font-size: 12px; }
      .grid-top { display: flex; gap: 12px; margin: 5px 0; }
      .cliente-box { flex: 1; border: 1px solid #333; padding: 6px; font-size: 10px; line-height: 1.3; }
      .vehiculo-img { width: 190px; height: 110px; border: 1px solid #ccc; display: flex; align-items: center; justify-content: center; background: #f0f0f0; font-size: 10px; overflow: hidden; }
      .vehiculo-img img { max-width: 100%; max-height: 100%; object-fit: contain; }
      .info-box { border: 1px solid #999; padding: 6px; margin: 6px 0; font-size: 10.5px; }
      .info-box div { margin-bottom: 2px; }
      .conditions-box { position: absolute; left: 1cm; right: 1cm; bottom: 3.2cm; font-size: 9px; margin: 0; text-align: justify; border-top: 1px dashed #003366; padding-top: 6px; max-height: 4.4cm; overflow: hidden; }
      .conditions-box p { margin: 0; font-weight: bold; }
      .footer-signs { position: absolute; left: 1cm; right: 1cm; bottom: 1cm; display: flex; justify-content: space-between; margin: 0; }
      .sign { border-top: 1px solid #000; width: 240px; text-align: center; padding-top: 4px; font-size: 10px; }
      .section-header, .info-box, .cliente-box, .vehiculo-img, .conditions-box, .footer-signs { page-break-inside: avoid; }
      @media screen { :host { background: #e9ecef; padding: 20px 0; } .page { box-shadow: 0 5px 15px rgba(0,0,0,0.1); } }
      @media print { :host { background: none; padding: 0; } .page { margin: 0; box-shadow: none; background: none; } }
    `;

    // TEMPLATE CON DATOS DINÁMICOS DEL SOCIO COMERCIAL
    const template = `
      <style>${styles}</style>
      <div class="page" id="pageContainer">
        <div class="content-flow">
          <div class="header-container">
            <div class="logo-box">
              <img src="${this._socioComercial.logo_url || '/dongfeng.png'}" alt="${this._socioComercial.razon_social || 'Dongfeng'}">
            </div>
            <div class="header-info">
              <h2>${this._socioComercial.razon_social || 'CAPITAL MOTORS, C.A'}</h2>
              <p>R.I.F.: ${this._socioComercial.rif || 'N/A'}</p><p class="header-dir">${this._formatDireccion(this._socioComercial.direccion, 55)}</p><p>TLF. ${this._socioComercial.telefono || ''} &nbsp;|&nbsp; ${this._socioComercial.correo || ''}</p>
            </div>
            <div class="header-right">
              <p style="color:#003366">PRESUPUESTO<br>N°: ${this._numeroPresupuesto}</p>
              <p>Fecha Emisión: ${this._fechaEmision}<br>Forma de Pago: <strong>${this._formaPago}</strong></p>
            </div>
          </div>
          <div class="grid-top">
            <div class="cliente-box">
              <strong>Cliente:</strong> ${this._cliente.nombre}<br>
              <strong>R.I.F/C.I:</strong> ${this._cliente.ci}<br>
              <strong>e-mail:</strong> ${this._cliente.email}<br>
              <strong>Teléfonos:</strong> ${this._cliente.telefono}<br>
              <strong>Vendedor:</strong> ${this._cliente.vendedor}
            </div>
            <div class="vehiculo-img">
              ${this._imagenAuto ? `<img src="${this._imagenAuto}" alt="Vehículo">` : 'IMAGEN AUTO'}
            </div>
          </div>
          <div class="section-header">DATOS DEL VEHICULO</div>
          <div class="info-box">${vehiculoHTML}</div>
          ${bloquesHTML}
        </div>
        <div class="conditions-box">
          <p>Planchart Global, C.A. es el Distribuidor Maestro Oficial de DONGFENG en Venezuela. Capital Motors forma parte de la red autorizada de concesionarios afiliados para comercialización, entrega y servicio postventa de la marca.</p>
        </div>
        <div class="footer-signs">
          <div class="sign">Asesor Comercial</div>
          <div class="sign">Cliente</div>
        </div>
      </div>
    `;
    this.shadowRoot.innerHTML = template;
  }

  _ajustarEscala() {
    const page = this.shadowRoot.querySelector('#pageContainer');
    if (!page) return;
    const content = this.shadowRoot.querySelector('.content-flow');
    if (!content) return;
    // Zona útil de la hoja Carta (27.94cm) por encima de condiciones (3.2cm+4.4cm) y firmas (1cm)
    const alturaUtilCM = 18.9;
    const alturaContenidoPX = content.scrollHeight;
    const pxPorCm = 37.795;
    const alturaContenidoCM = alturaContenidoPX / pxPorCm;
    if (alturaContenidoCM > alturaUtilCM) {
      const factor = alturaUtilCM / alturaContenidoCM;
      const factorFinal = factor * 0.95;
      content.style.transform = `scale(${factorFinal})`;
      content.style.transformOrigin = 'top center';
    } else {
      content.style.transform = '';
    }
  }

  // Escala la hoja para que SIEMPRE quepa y quede centrada en el contenedor
  // visible del modal, sea cual sea el ancho de pantalla. El contenedor puede
  // ser #modalCotizacionContent (nueva-cotizacion/listar) o #impWcWrap
  // (editar-json / CotizacionImpresion.astro).
  _ajustarEscalaAncho() {
    const page = this.shadowRoot.querySelector('#pageContainer');
    if (!page) return;
    const contenido = this.closest('#modalCotizacionContent, #impWcWrap') || this.parentElement;
    if (!contenido) return;
    const anchoDisponible = contenido.clientWidth;
    const anchoHoja = 816; // Carta: 21.59cm ≈ 816px
    let factor = 1;
    if (anchoDisponible > 0 && anchoDisponible < anchoHoja) {
      factor = (anchoDisponible - 8) / anchoHoja; // margen de 4px c/lado
      if (factor < 0.2) factor = 0.2;
    }
    if (factor >= 1) {
      page.style.transform = '';
      page.style.transformOrigin = 'center top';
    } else {
      page.style.transform = `scale(${factor})`;
      page.style.transformOrigin = 'center top';
    }
  }
}

if (!customElements.get('capital-motors-cotizacion')) {
  customElements.define('capital-motors-cotizacion', CapitalMotorsCotizacion);
}
