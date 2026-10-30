'use client'
import { PictureOutlined } from '@ant-design/icons'
import { App, Button, Upload } from 'antd'
import ImgCrop from 'antd-img-crop'
import { useState } from 'react'
import { errMsg } from '@/lib/format'

/**
 * Image picker with in-browser cropping and compression, then upload.
 *
 * aspect: crop ratio (width / height); undefined skips cropping. GIFs are never
 * cropped (that would drop the animation). Cropped images are scaled down to
 * maxWidth and re-encoded (WebP, or JPEG where the browser can't encode WebP),
 * so a phone photo of several MB ends up at a few hundred KB.
 */
export function ImageUpload({
  aspect,
  maxWidth,
  upload,
  onUploaded,
  maxBytes = 2 << 20,
}: {
  aspect?: number
  maxWidth: number
  upload: (file: File) => Promise<string>
  onUploaded: (url: string) => void
  maxBytes?: number
}) {
  const { message } = App.useApp()
  const [busy, setBusy] = useState(false)

  const send = async (file: File) => {
    setBusy(true)
    try {
      const out = file.type === 'image/gif' ? file : await shrink(file, maxWidth)
      if (out.size > maxBytes) {
        message.error(`图片压缩后仍有 ${(out.size / 1048576).toFixed(1)} MB，请换一张或裁小一些（上限 ${maxBytes / 1048576} MB）`)
        return
      }
      onUploaded(await upload(out))
      message.success(`图片已上传（${Math.max(1, Math.round(out.size / 1024))} KB）`)
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  const picker = (
    <Upload
      accept="image/png,image/jpeg,image/webp,image/gif"
      showUploadList={false}
      beforeUpload={(f) => {
        void send(f)
        return false
      }}
    >
      <Button icon={<PictureOutlined />} loading={busy}>{aspect ? '上传并裁剪图片' : '上传图片'}</Button>
    </Upload>
  )
  if (!aspect) return picker
  return (
    <ImgCrop
      aspect={aspect}
      rotationSlider
      showReset
      quality={0.95}
      modalTitle="裁剪图片"
      modalOk="确定"
      modalCancel="取消"
      resetText="重置"
      modalWidth={720}
      beforeCrop={(f) => f.type !== 'image/gif'}
    >
      {picker}
    </ImgCrop>
  )
}

/** Scale to at most maxWidth and re-encode; returns the original if that is smaller. */
async function shrink(file: File, maxWidth: number): Promise<File> {
  const bmp = await createImageBitmap(file)
  const scale = Math.min(1, maxWidth / bmp.width)
  const w = Math.round(bmp.width * scale)
  const h = Math.round(bmp.height * scale)
  const canvas = document.createElement('canvas')
  canvas.width = w
  canvas.height = h
  const ctx = canvas.getContext('2d')!
  ctx.imageSmoothingQuality = 'high'
  ctx.drawImage(bmp, 0, 0, w, h)
  bmp.close()
  const encode = (type: string, q: number) => new Promise<Blob | null>((r) => canvas.toBlob(r, type, q))
  let blob = await encode('image/webp', 0.86)
  if (!blob || blob.type !== 'image/webp') {
    // Safari can't encode WebP; JPEG has no alpha, so paint transparent areas white.
    ctx.globalCompositeOperation = 'destination-over'
    ctx.fillStyle = '#fff'
    ctx.fillRect(0, 0, w, h)
    blob = await encode('image/jpeg', 0.86)
  }
  if (!blob || (blob.size >= file.size && scale === 1)) return file
  const ext = blob.type === 'image/webp' ? 'webp' : 'jpg'
  return new File([blob], file.name.replace(/\.[^.]+$/, '') + '.' + ext, { type: blob.type })
}
