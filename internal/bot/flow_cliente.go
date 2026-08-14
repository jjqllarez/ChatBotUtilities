package bot

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"bot/internal/cotizaciones"
	"bot/internal/empleados"
)

const (
	stepRegCedula = "reg_cliente_cedula"
	stepRegNombre = "reg_cliente_nombre"
)

// RegistrarClienteFlow implementa la interfaz Flow para la creación de un nuevo cliente.
// Puede ejecutarse de forma autónoma (vendedor pide registrar cliente) o como sub-rutina de /cotizar.
type RegistrarClienteFlow struct {
	b *Bot
}

// NewRegistrarClienteFlow crea la instancia del flujo de registro de clientes.
func NewRegistrarClienteFlow(b *Bot) *RegistrarClienteFlow {
	return &RegistrarClienteFlow{b: b}
}

func (f *RegistrarClienteFlow) Nombre() string { return "registrar_cliente" }
func (f *RegistrarClienteFlow) Tipo() FlowTipo { return FlowComposable }

func (f *RegistrarClienteFlow) Activo(ctx context.Context, phone string) bool {
	state, _ := f.b.state.Get(ctx, phone)
	step, ok := state["reg_cliente_step"].(string)
	return ok && step != ""
}

func (f *RegistrarClienteFlow) Iniciar(phone string, emp *empleados.Empleado) {
	chat := types.NewJID(phone, types.DefaultUserServer)
	f.b.setStateKey(context.Background(), phone, "reg_cliente_step", stepRegCedula)
	f.b.sendText(chat, "📌 *Registro de Cliente Nuevo*\nIndica el número de cédula o RIF del cliente (ej. V-12345678 o J-123456789):")
}

func (f *RegistrarClienteFlow) IniciarComo(phone string, emp *empleados.Empleado, params map[string]any) {
	chat := types.NewJID(phone, types.DefaultUserServer)
	if cedula, ok := params["cedula"].(string); ok && cedula != "" {
		f.b.setStateKey(context.Background(), phone, "reg_cliente_cedula", cedula)
		f.b.setStateKey(context.Background(), phone, "reg_cliente_step", stepRegNombre)
		f.b.sendText(chat, fmt.Sprintf("📌 *Registro de Cliente Nuevo*\nCédula: %s\nIndica el nombre o razón social del cliente:", cedula))
		return
	}
	f.Iniciar(phone, emp)
}

func (f *RegistrarClienteFlow) Procesar(ctx context.Context, phone string, emp *empleados.Empleado, text string) (FlowResult, error) {
	chat := types.NewJID(phone, types.DefaultUserServer)
	state, _ := f.b.state.Get(ctx, phone)
	step, _ := state["reg_cliente_step"].(string)

	switch step {
	case stepRegCedula:
		cedula := strings.TrimSpace(text)
		if cedula == "" {
			f.b.sendText(chat, "Indica un número de cédula o RIF válido.")
			return FlowResult{}, nil
		}
		f.b.setStateKey(ctx, phone, "reg_cliente_cedula", cedula)
		f.b.setStateKey(ctx, phone, "reg_cliente_step", stepRegNombre)
		f.b.sendText(chat, "Indica el nombre completo o razón social del cliente:")
		return FlowResult{}, nil

	case stepRegNombre:
		nombre := strings.TrimSpace(text)
		if nombre == "" {
			f.b.sendText(chat, "Indica un nombre o razón social válido.")
			return FlowResult{}, nil
		}
		cedula, _ := state["reg_cliente_cedula"].(string)
		tipoDoc := "V"
		if strings.HasPrefix(strings.ToUpper(cedula), "J") {
			tipoDoc = "J"
		} else if strings.HasPrefix(strings.ToUpper(cedula), "E") {
			tipoDoc = "E"
		} else if strings.HasPrefix(strings.ToUpper(cedula), "G") {
			tipoDoc = "G"
		}

		id, err := cotizaciones.CrearCliente(ctx, f.b.supa, emp.SocioComercial, cotizaciones.CrearClienteParams{
			TipoDocumento:     tipoDoc,
			NumeroDocumento:   cedula,
			NombreRazonSocial: nombre,
			TelefonoPrincipal: phone,
		})
		f.Cancelar(phone)

		if err != nil {
			f.b.sendText(chat, "❌ Error guardando el cliente: "+err.Error())
			return FlowResult{}, err
		}

		f.b.sendText(chat, fmt.Sprintf("✅ Cliente *%s* (ID %d, Doc: %s) registrado con éxito.", nombre, id, cedula))
		return FlowResult{
			Completado: true,
			Datos: map[string]any{
				"cliente_id": id,
				"nombre":     nombre,
				"cedula":     cedula,
			},
		}, nil
	}

	return FlowResult{}, nil
}

func (f *RegistrarClienteFlow) Cancelar(phone string) {
	ctx := context.Background()
	f.b.clearStateKey(ctx, phone, "reg_cliente_step")
	f.b.clearStateKey(ctx, phone, "reg_cliente_cedula")
}

func (f *RegistrarClienteFlow) StepHint(ctx context.Context, phone string) string {
	state, _ := f.b.state.Get(ctx, phone)
	step, _ := state["reg_cliente_step"].(string)
	if step == stepRegCedula {
		return "El usuario está registrando un cliente y debe ingresar la cédula/RIF."
	}
	if step == stepRegNombre {
		return "El usuario está registrando un cliente y debe ingresar el nombre o razón social."
	}
	return ""
}

// setStateKey helper para guardar un valor individual en el StateStore.
func (b *Bot) setStateKey(ctx context.Context, phone, key, val string) {
	state, _ := b.state.Get(ctx, phone)
	state[key] = val
	_ = b.state.Set(ctx, phone, state)
}
