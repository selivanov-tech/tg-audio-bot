package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/selivanov-tech/tg-audio-bot/internal/mediabot/domain"
)

// Russian user-facing strings. Single source of truth — edit here, not at
// call sites. English is not a supported locale; there is no fallback.

// Fixed text.
const (
	MsgHelp = "Отправьте ссылку на YouTube — я скачаю аудио.\n\n" +
		"Можно задать имя файла: /download <url> <имя>"
	MsgDownloadUsage     = "Как использовать: /download <url> [имя]"
	MsgUnsupportedURL    = "Ссылка не поддерживается. Сейчас доступен только YouTube."
	MsgRouteFailed       = "Не удалось обработать ссылку. Попробуйте ещё раз."
	MsgFetching          = "Получаю информацию о видео..."
	MsgRetryLowerQuality = "Попробовать в MP3? Файл будет меньше."
	MsgStaleSelection    = "Эта кнопка больше не активна. Отправьте ссылку ещё раз."
	MsgSending           = "Отправляю аудио..."
	MsgSendFailed        = "Не удалось отправить аудио. Попробуйте ещё раз."
	MsgInternalError     = "Внутренняя ошибка. Попробуйте ещё раз."
	MsgAccessDenied      = "Доступ к боту ограничен."
)

// QualityLabel returns the user-facing button text for a quality tier.
// Kept in the application layer so domain has no UI concerns.
func QualityLabel(q domain.Quality) string {
	switch q {
	case domain.QualityBest:
		return "Лучшее (M4A)"
	case domain.QualityGood:
		return "Хорошее (MP3)"
	default:
		return "Неизвестно"
	}
}

// RenderProgress turns a domain.Progress snapshot into a Telegram message body.
// Single renderer for all progress ticks (including the initial "0%" before
// yt-dlp emits any real progress line).
func RenderProgress(p domain.Progress) string {
	if p.Converting {
		return "Конвертирую аудио... (для длинных видео нужна минута)"
	}
	if p.Percent >= 100 {
		return "Скачиваю... 100%"
	}
	if p.TotalSize == "" {
		return fmt.Sprintf("Скачиваю... %.1f%%", p.Percent)
	}
	return fmt.Sprintf("Скачиваю... %.1f%% из %s (%s, ETA %s)",
		p.Percent, p.TotalSize, p.Speed, p.ETA)
}

// MsgQueued renders the "waiting in queue" status.
func MsgQueued(position int) string {
	return fmt.Sprintf("В очереди (позиция %d). Начну после текущей загрузки.", position)
}

// MsgVideoTooLong renders the duration-limit rejection shown before any
// download starts. Both durations are minutes, rounded up from the provider's
// reported duration so a 45:30 video reads as "46" not "45".
func MsgVideoTooLong(actualMin, limitMin int) string {
	return fmt.Sprintf("Видео длится %d мин — лимит %d мин. Отправьте более короткое видео.",
		actualMin, limitMin)
}

// MsgTooLarge renders the 50MB-limit rejection. When the file came from the
// Best tier, the MP3 retry is offered separately via MsgRetryLowerQuality and
// its keyboard, so no advice is baked in here.
func MsgTooLarge(sizeMB int64) string {
	return fmt.Sprintf("Файл %d МБ — превышает лимит Telegram в 50 МБ.", sizeMB)
}

// Stage identifies which lifecycle phase produced an error, so MapUserError
// can return stage-appropriate Russian text for the same underlying cause.
type Stage int

const (
	StageFetch Stage = iota
	StageDownload
	StageSend
)

// MapUserError turns an error into a short Russian message for the user.
// Raw err.Error() is never surfaced; detail stays in the logs. Generic
// failures collapse into one safe fallback per stage.
func MapUserError(stage Stage, err error) string {
	if err == nil {
		return ""
	}

	if errors.Is(err, context.DeadlineExceeded) {
		switch stage {
		case StageFetch:
			return "Не дождались ответа от YouTube. Попробуйте через минуту."
		case StageDownload:
			return "Скачивание заняло слишком много времени. Попробуйте ещё раз."
		case StageSend:
			return "Отправка заняла слишком много времени. Попробуйте ещё раз."
		}
	}

	if errors.Is(err, domain.ErrFileTooLarge) {
		// Size-specific message is built via MsgTooLarge at the call site;
		// reaching here means the caller didn't pre-format, so fall through
		// to a safe generic.
		return "Файл слишком большой для Telegram. Попробуйте качество «Хорошее»."
	}

	low := strings.ToLower(err.Error())

	// YouTube bot-check / auth paths (fetch stage in practice).
	if strings.Contains(low, "confirm you") && strings.Contains(low, "bot") {
		return "YouTube требует авторизации (защита от ботов). Нужно обновить cookies."
	}
	if strings.Contains(low, "sign in") || strings.Contains(low, "cookies") {
		return "YouTube требует авторизации. Нужно обновить cookies."
	}
	if strings.Contains(low, "deadline exceeded") {
		// Wrapped DeadlineExceeded that errors.Is didn't catch.
		return "Не дождались ответа от YouTube. Попробуйте через минуту."
	}

	// Telegram send-path privacy rejection. Should not trigger after the
	// opus→m4a switch, but kept as a guard.
	if strings.Contains(low, "voice_messages_forbidden") {
		return "Получатель ограничил приём голосовых сообщений. Попробуйте качество «Хорошее»."
	}

	// Stage-specific generic fallback.
	switch stage {
	case StageFetch:
		return "Не удалось получить информацию. Видео может быть приватным или недоступным в регионе."
	case StageDownload:
		return "Не удалось скачать. Попробуйте ещё раз."
	case StageSend:
		return MsgSendFailed
	}
	return MsgInternalError
}
