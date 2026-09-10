package cobranzas

import "testing"

func testCuotas() []Cuota {
	return []Cuota{
		{ID: 1, CreditoID: 10, NumeroGiro: "Inicial", MontoProgramado: 200, FechaVencimiento: "2026-09-10", EstadoCuota: "Pendiente", NumeroFactura: "TEST-COB-001", ClienteNombre: "Fredy Trujillo", ClienteDocumento: "00000000"},
		{ID: 2, CreditoID: 10, NumeroGiro: "1/2", MontoProgramado: 400, FechaVencimiento: "2026-10-10", EstadoCuota: "Pendiente", NumeroFactura: "TEST-COB-001", ClienteNombre: "Fredy Trujillo", ClienteDocumento: "00000000"},
		{ID: 3, CreditoID: 10, NumeroGiro: "2/2", MontoProgramado: 400, FechaPago: "2026-09-08", MontoPagado: 400, EstadoCuota: "Pagado", NumeroFactura: "TEST-COB-001", ClienteNombre: "Fredy Trujillo", ClienteDocumento: "00000000"},
		{ID: 4, CreditoID: 11, NumeroGiro: "1/2", MontoProgramado: 500, FechaVencimiento: "2026-08-01", EstadoCuota: "Vencido", NumeroFactura: "FAC-002", ClienteNombre: "Maria Perez", ClienteDocumento: "V-123"},
	}
}

func TestEsVencida(t *testing.T) {
	c := testCuotas()
	cases := []struct {
		id   int64
		want bool
	}{
		{1, false}, // vence hoy (no vencida)
		{2, false},
		{3, false}, // pagada
		{4, true},  // vencida
	}
	for _, tc := range cases {
		var cuota Cuota
		for _, x := range c {
			if x.ID == tc.id {
				cuota = x
			}
		}
		if got := esVencida(cuota, "2026-09-10"); got != tc.want {
			t.Errorf("esVencida(id=%d) = %v, quería %v", tc.id, got, tc.want)
		}
	}
}

func TestPorVencer(t *testing.T) {
	c := testCuotas()
	list := PorVencer(c, 30)
	if len(list) != 2 {
		t.Errorf("PorVencer(30) = %d, quería 2", len(list))
	}
	list7 := PorVencer(c, 7)
	if len(list7) != 1 { // solo la que vence hoy
		t.Errorf("PorVencer(7) = %d, quería 1", len(list7))
	}
}

func TestVencidos(t *testing.T) {
	list := Vencidos(testCuotas())
	if len(list) != 1 {
		t.Errorf("Vencidos = %d, quería 1", len(list))
	}
}

func TestPagados(t *testing.T) {
	list := Pagados(testCuotas(), "2026-09-01", "2026-09-30")
	if len(list) != 1 {
		t.Errorf("Pagados sep = %d, quería 1", len(list))
	}
}

func TestResumir(t *testing.T) {
	r := Resumir(testCuotas())
	if r.VencidosCount != 1 || r.PorVencerCount != 2 || r.PagadosCount != 1 {
		t.Errorf("Resumir inesperado: %+v", r)
	}
	if r.CreditosActivos != 2 {
		t.Errorf("CreditosActivos = %d, quería 2", r.CreditosActivos)
	}
}

func TestPorCliente(t *testing.T) {
	list := PorCliente(testCuotas(), "fredy")
	if len(list) != 3 {
		t.Errorf("PorCliente fredy = %d, quería 3", len(list))
	}
	if len(PorCliente(testCuotas(), "V-123")) != 1 {
		t.Errorf("PorCliente por doc falló")
	}
}

func TestPorCredito(t *testing.T) {
	list := PorCredito(testCuotas(), "COB")
	if len(list) != 3 {
		t.Errorf("PorCredito COB = %d, quería 3", len(list))
	}
}

func TestRangoMes(t *testing.T) {
	desde, hasta := RangoMes(0)
	if len(desde) != 10 || len(hasta) != 10 {
		t.Errorf("RangoMes format inválido: %s %s", desde, hasta)
	}
	if desde > hasta {
		t.Errorf("RangoMes invertido: %s > %s", desde, hasta)
	}
}
