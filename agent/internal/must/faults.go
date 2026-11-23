package must

import "strings"

// Bit tables for the fault/warning registers, from the PV1800 map in github.com/mukaschultze/ha-must-inverter
// (const.py; bit N = list index N, LSB first). The upstream inverter-error list has a missing comma at bit 15,
// fixed here; bits 16..31 are assumed to live in the next register (25262). Texts translated.

// InverterErrors: 25261 (bits 0..15) + 25262 (bits 16..31, assumed).
var InverterErrors = []string{
	"вентилятор заблокирован при выключенном инверторе",
	"перегрев трансформатора инвертора",
	"напряжение батареи слишком высокое",
	"напряжение батареи слишком низкое",
	"короткое замыкание на выходе",
	"высокое выходное напряжение инвертора",
	"перегрузка (таймаут)",
	"напряжение шины слишком высокое",
	"не удался мягкий старт шины",
	"отказ главного реле",
	"ошибка датчика выходного напряжения",
	"ошибка датчика напряжения сети",
	"ошибка датчика выходного тока",
	"ошибка датчика тока сети",
	"ошибка датчика тока нагрузки",
	"перегрузка по току сети",
	"перегрев радиатора инвертора",
	"ошибка класса напряжения батареи (солнечный контроллер)",
	"ошибка датчика тока солнечного контроллера",
	"ток солнечного контроллера неуправляем",
	"низкое напряжение сети",
	"высокое напряжение сети",
	"частота сети ниже нормы",
	"частота сети выше нормы",
	"защита инвертора по току",
	"напряжение шины слишком низкое",
	"не удался мягкий старт инвертора",
	"постоянное напряжение на выходе AC",
	"обрыв подключения батареи",
	"ошибка датчика управляющего тока",
	"выходное напряжение инвертора слишком низкое",
	"",
}

// InverterWarnings: 25265.
var InverterWarnings = []string{
	"вентилятор заблокирован при работе инвертора",
	"вентилятор 2 заблокирован при работе инвертора",
	"батарея перезаряжена",
	"низкий заряд батареи",
	"перегрузка",
	"снижение выходной мощности",
	"солнечный контроллер остановлен: низкое напряжение батареи",
	"солнечный контроллер остановлен: высокое напряжение панелей",
	"солнечный контроллер остановлен: перегрузка",
	"перегрев солнечного контроллера",
	"ошибка связи с солнечным контроллером",
	"", "", "", "", "",
}

// ChargerErrors: 15213.
var ChargerErrors = []string{
	"аппаратная защита", "перегрузка по току", "ошибка датчика тока", "перегрев",
	"напряжение панелей слишком высокое", "напряжение панелей слишком низкое",
	"напряжение батареи слишком высокое", "напряжение батареи слишком низкое",
	"ток неуправляем", "ошибка параметров", "", "", "", "", "", "",
}

// QuietChargerErrors are charger error bits that are normal (bit 5 "PV voltage too low" every night):
// logged as events, never notified.
const QuietChargerErrors uint32 = 1 << 5

// ChargerWarnings: 15214.
var ChargerWarnings = []string{"ошибка вентилятора", "", "", "", "", "", "", "", "", "", "", "", "", "", "", ""}

// BitTexts lists the texts of the set bits in mask (unknown bits as "бит N").
func BitTexts(mask uint32, table []string) []string {
	var out []string
	for i := 0; i < 32; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		if i < len(table) && table[i] != "" {
			out = append(out, table[i])
		} else {
			out = append(out, "бит "+itoa(i))
		}
	}
	return out
}

// Describe joins BitTexts with "; " or returns "нет".
func Describe(mask uint32, table []string) string {
	if mask == 0 {
		return "нет"
	}
	return strings.Join(BitTexts(mask, table), "; ")
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
