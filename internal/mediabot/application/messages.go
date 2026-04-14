package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rag-brothers/media-bot/internal/mediabot/domain"
)

// Russian user-facing strings. Single source of truth — edit here, not at
// call sites. English is not a supported locale; there is no fallback.

// Fixed text.
const (
	MsgHelp = "Отправьте ссылку на YouTube — я скачаю аудио.\n\n" +
		"Можно задать имя файла: /download <url> <имя>"
	MsgDownloadUsage    = "Как использовать: /download <url> [имя]"
	MsgUnsupportedURL   = "Ссылка не поддерживается. Сейчас доступен только YouTube."
	MsgRouteFailed      = "Не удалось обработать ссылку. Попробуйте ещё раз."
	MsgFetching         = "Получаю информацию о видео..."
	MsgChooseQuality    = "Выберите качество:"
	MsgStaleSelection   = "Эта кнопка больше не активна. Отправьте ссылку ещё раз."
	MsgSending          = "Отправляю аудио..."
	MsgSendFailed       = "Не удалось отправить аудио. Попробуйте ещё раз."
	MsgInternalError    = "Внутренняя ошибка. Попробуйте ещё раз."
	MsgQualityBestLabel = "Лучшее (M4A)"
	MsgQualityGoodLabel = "Хорошее (MP3)"
)

// MsgQueued renders the "waiting in queue" status.
func MsgQueued(position int) string {
	return fmt.Sprintf("В очереди (позиция %d). Начну после текущей загрузки.", position)
}

// MsgTooLarge renders the 50MB-limit rejection. suggestGood is true when the
// user picked Best quality and Good might fit under the cap.
func MsgTooLarge(sizeMB int64, suggestGood bool) string {
	msg := fmt.Sprintf("Файл %d МБ — превышает лимит Telegram в 50 МБ.", sizeMB)
	if suggestGood {
		msg += " Попробуйте качество «Хорошее»."
	}
	return msg
}

// MsgProgressDownloading renders a mid-download progress tick.
func MsgProgressDownloading(pct float64, totalSize, speed, eta string) string {
	if totalSize == "" {
		return fmt.Sprintf("Скачиваю... %.1f%%", pct)
	}
	return fmt.Sprintf("Скачиваю... %.1f%% из %s (%s, ETA %s)", pct, totalSize, speed, eta)
}

// MsgProgressDone is shown at 100% download before post-processing begins.
const MsgProgressDone = "Скачиваю... 100%"

// MsgProgressConverting is shown once yt-dlp enters audio extraction / transmux.
const MsgProgressConverting = "Конвертирую аудио... (для длинных видео нужна минута)"

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
