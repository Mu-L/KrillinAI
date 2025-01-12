package service

import (
	"bufio"
	"bytes"
	"fmt"
	"go.uber.org/zap"
	"krillin-ai/internal/storage"
	"krillin-ai/internal/types"
	"krillin-ai/log"
	"krillin-ai/pkg/util"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func splitMajorTextInHorizontal(text string) []string {
	// 按语言情况分割
	var segments []string
	containsAlphabetic := util.ContainsAlphabetic(text)
	if !containsAlphabetic {
		segments = regexp.MustCompile(`.`).FindAllString(text, -1)
	} else {
		segments = strings.Split(text, " ")
	}

	totalWidth := len(segments)

	// 直接返回原句子
	if (containsAlphabetic && totalWidth <= 10) || (!containsAlphabetic && totalWidth <= 20) {
		return []string{text}
	}

	// 确定拆分点，按2/5和3/5的比例拆分
	line1MaxWidth := int(float64(totalWidth) * 2 / 5)
	currentWidth := 0
	splitIndex := 0

	for i, _ := range segments {
		currentWidth++

		// 当达到 2/5 宽度时，设置拆分点
		if currentWidth >= line1MaxWidth {
			splitIndex = i + 1
			break
		}
	}

	// 分割文本，保留原有句子格式
	line1 := strings.Join(segments[:splitIndex], "")
	line2 := strings.Join(segments[splitIndex:], "")
	line1 = util.CleanPunction(line1)
	line2 = util.CleanPunction(line2)

	return []string{line1, line2}
}

func splitChineseText(text string, maxWordLine int) []string {
	var lines []string
	words := []rune(text)
	for i := 0; i < len(words); i += maxWordLine {
		end := i + maxWordLine
		if end > len(words) {
			end = len(words)
		}
		lines = append(lines, string(words[i:end]))
	}
	return lines
}

func parseSRTTime(timeStr string) (time.Duration, error) {
	timeStr = strings.Replace(timeStr, ",", ".", 1)
	parts := strings.Split(timeStr, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid time format: %s", timeStr)
	}

	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	secondsAndMilliseconds := strings.Split(parts[2], ".")
	if len(secondsAndMilliseconds) != 2 {
		return 0, fmt.Errorf("invalid time format: %s", timeStr)
	}
	seconds, err := strconv.Atoi(secondsAndMilliseconds[0])
	if err != nil {
		return 0, err
	}
	milliseconds, err := strconv.Atoi(secondsAndMilliseconds[1])
	if err != nil {
		return 0, err
	}

	duration := time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second +
		time.Duration(milliseconds)*time.Millisecond

	return duration, nil
}

func formatTimestamp(t time.Duration) string {
	hours := int(t.Hours())
	minutes := int(t.Minutes()) % 60
	seconds := int(t.Seconds()) % 60
	milliseconds := int(t.Milliseconds()) % 1000 / 10
	return fmt.Sprintf("%02d:%02d:%02d.%02d", hours, minutes, seconds, milliseconds)
}

func srtToAss(inputSRT, outputASS string, isHorizontal bool) error {
	file, err := os.Open(inputSRT)
	if err != nil {
		return err
	}
	defer file.Close()

	assFile, err := os.Create(outputASS)
	if err != nil {
		return err
	}
	defer assFile.Close()
	scanner := bufio.NewScanner(file)

	if isHorizontal {
		_, _ = assFile.WriteString(types.AssHeaderHorizontal)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			// 读取时间戳行
			if !scanner.Scan() {
				break
			}
			timestampLine := scanner.Text()
			parts := strings.Split(timestampLine, " --> ")
			if len(parts) != 2 {
				continue // 无效时间戳格式
			}

			startTimeStr := strings.TrimSpace(parts[0])
			endTimeStr := strings.TrimSpace(parts[1])
			startTime, err := parseSRTTime(startTimeStr)
			if err != nil {
				return err
			}
			endTime, err := parseSRTTime(endTimeStr)
			if err != nil {
				return err
			}

			var subtitleLines []string
			for scanner.Scan() {
				textLine := scanner.Text()
				if textLine == "" {
					break // 字幕块结束
				}
				subtitleLines = append(subtitleLines, textLine)
			}

			if len(subtitleLines) < 2 {
				continue
			}
			majorLine := strings.Join(splitMajorTextInHorizontal(subtitleLines[0]), "      \\N")
			minorLine := util.CleanPunction(subtitleLines[1])

			// ASS条目
			startFormatted := formatTimestamp(startTime)
			endFormatted := formatTimestamp(endTime)
			combinedText := fmt.Sprintf("{\\an2}{\\rMajor}%s\\N{\\rMinor}%s", majorLine, minorLine)
			_, _ = assFile.WriteString(fmt.Sprintf("Dialogue: 0,%s,%s,Major,,0,0,0,,%s\n", startFormatted, endFormatted, combinedText))
		}
	} else {
		_, _ = assFile.WriteString(types.AssHeaderVertical)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if !scanner.Scan() {
				break
			}
			timestampLine := scanner.Text()
			parts := strings.Split(timestampLine, " --> ")
			if len(parts) != 2 {
				continue // 无效时间戳格式
			}

			startTimeStr := strings.TrimSpace(parts[0])
			endTimeStr := strings.TrimSpace(parts[1])
			startTime, err := parseSRTTime(startTimeStr)
			if err != nil {
				return err
			}
			endTime, err := parseSRTTime(endTimeStr)
			if err != nil {
				return err
			}

			var content string
			scanner.Scan()
			content = scanner.Text()
			if content == "" {
				continue
			}
			totalTime := endTime - startTime

			if !util.ContainsAlphabetic(content) {
				// 处理中文字幕
				chineseLines := splitChineseText(content, 10)
				for i, line := range chineseLines {
					iStart := startTime + time.Duration(float64(i)*float64(totalTime)/float64(len(chineseLines)))
					iEnd := startTime + time.Duration(float64(i+1)*float64(totalTime)/float64(len(chineseLines)))
					if iEnd > endTime {
						iEnd = endTime
					}

					startFormatted := formatTimestamp(iStart)
					endFormatted := formatTimestamp(iEnd)
					cleanedText := util.CleanPunction(line)
					combinedText := fmt.Sprintf("{\\an2}{\\rMajor}%s", cleanedText)
					_, _ = assFile.WriteString(fmt.Sprintf("Dialogue: 0,%s,%s,Major,,0,0,0,,%s\n", startFormatted, endFormatted, combinedText))
				}
			} else {
				// 处理英文字幕
				startFormatted := formatTimestamp(startTime)
				endFormatted := formatTimestamp(endTime)
				cleanedText := util.CleanPunction(content)
				combinedText := fmt.Sprintf("{\\an2}{\\rMinor}%s", cleanedText)
				_, _ = assFile.WriteString(fmt.Sprintf("Dialogue: 0,%s,%s,Minor,,0,0,0,,%s\n", startFormatted, endFormatted, combinedText))
			}
		}
	}
	return nil
}

func embedSubtitles(videoFilePath, srtFilePath, workDir string, isHorizontal bool) error {
	outputFileName := types.SubtitleTaskVerticalEmbedVideoFileName
	assPath := filepath.Join(workDir, "formatted_subtitles.ass")

	if isHorizontal {
		outputFileName = types.SubtitleTaskHorizontalEmbedVideoFileName
	}
	if err := srtToAss(srtFilePath, assPath, isHorizontal); err != nil {
		return err
	}

	cmd := exec.Command(storage.FfmpegPath, "-y", "-i", videoFilePath, "-vf", fmt.Sprintf("ass=%s", strings.ReplaceAll(assPath, "\\", "/")), "-c:a", "copy", filepath.Join(workDir, fmt.Sprintf("/output/%s", outputFileName)))
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.GetLogger().Error("embedSubtitles ffmpeg err", zap.Any("step param", videoFilePath), zap.String("output", string(output)), zap.Error(err))
		return err
	}
	return nil
}

func getFontPaths() (string, string, error) {
	switch runtime.GOOS {
	case "windows":
		return "C\\:/Windows/Fonts/msyhbd.ttc", "C\\:/Windows/Fonts/msyh.ttc", nil // 在ffmpeg参数里必须这样写
	case "darwin":
		return "/System/Library/Fonts/Supplemental/Arial Bold.ttf", "/System/Library/Fonts/Supplemental/Arial.ttf", nil
	case "linux":
		return "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", nil
	default:
		return "", "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func getResolution(inputVideo string) (int, int, error) {
	// 获取视频信息
	cmdArgs := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=s=x:p=0",
		inputVideo,
	}
	cmd := exec.Command(storage.FfprobePath, cmdArgs...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		log.GetLogger().Error("获取视频分辨率失败", zap.String("output", out.String()), zap.Error(err))
		return 0, 0, err
	}

	output := strings.TrimSpace(out.String())
	dimensions := strings.Split(output, "x")
	if len(dimensions) != 2 {
		log.GetLogger().Error("获取视频分辨率失败", zap.String("output", output))
		return 0, 0, fmt.Errorf("invalid resolution format: %s", output)
	}
	width, _ := strconv.Atoi(dimensions[0])
	height, _ := strconv.Atoi(dimensions[1])
	return width, height, nil
}

func convertToVertical(inputVideo, outputVideo, majorTitle, minorTitle string) error {
	if _, err := os.Stat(outputVideo); err == nil {
		log.GetLogger().Info("竖屏视频已存在", zap.String("outputVideo", outputVideo))
		return nil
	}

	fontBold, fontRegular, err := getFontPaths()
	if err != nil {
		log.GetLogger().Error("获取字体路径失败", zap.Error(err))
		return err
	}

	cmdArgs := []string{
		"-i", inputVideo,
		"-vf", fmt.Sprintf("scale=720:1280:force_original_aspect_ratio=decrease,pad=720:1280:(ow-iw)/2:(oh-ih)*2/5,drawbox=y=0:h=100:c=black@1:t=fill,drawtext=text='%s':x=(w-text_w)/2:y=210:fontsize=55:fontcolor=yellow:box=1:boxcolor=black@0.5:fontfile='%s',drawtext=text='%s':x=(w-text_w)/2:y=280:fontsize=40:fontcolor=yellow:box=1:boxcolor=black@0.5:fontfile='%s'",
			majorTitle, fontBold, minorTitle, fontRegular),
		"-r", "30",
		"-b:v", "7587k",
		"-c:a", "aac",
		"-b:a", "192k",
		"-c:v", "libx264",
		"-preset", "fast",
		"-y",
		outputVideo,
	}
	cmd := exec.Command(storage.FfmpegPath, cmdArgs...)
	var output []byte
	output, err = cmd.CombinedOutput()
	if err != nil {
		log.GetLogger().Error("视频转竖屏失败", zap.String("output", string(output)), zap.Error(err))
		return err
	}

	fmt.Printf("竖屏视频已保存到: %s\n", outputVideo)
	return nil
}
