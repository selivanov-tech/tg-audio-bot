package telegram

import (
	"github.com/go-telegram/bot/models"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/application/port"
)

// buildInlineKeyboard converts a flat list of options into a one-row inline
// keyboard. Kept simple - all current use cases (quality picker) fit one row.
func buildInlineKeyboard(opts []port.KeyboardOption) *models.InlineKeyboardMarkup {
	row := make([]models.InlineKeyboardButton, 0, len(opts))
	for _, o := range opts {
		row = append(row, models.InlineKeyboardButton{
			Text:         o.Label,
			CallbackData: o.CallbackData,
		})
	}
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{row},
	}
}
