# ### 创建模型
import os
import time

import torch
from einops import rearrange
from torch import nn

audio_list = {
    'hubert': [10, 1024],
    'tiny': [50 // 5, 384 * 5]
}

face_list = {
    '96': {
        'down': [
            (16, 32, 1),
            (32, 64, 2),
            (64, 128, 2),
            (128, 256, 2),
            (256, 512, 2),
            (512, 512, 2),
        ],
        'up': [
            (1024, 512, 2, 1),
            (1024, 512, 2, 1),
            (768, 384, 2, 1),
            (512, 256, 2, 1),
            (320, 128, 2, 1),
            (160, 64, 1, 0),
        ],
        'sr_out': 64
    },
    '128': {
        'down': [
            (16, 32, 1),
            (32, 64, 2),
            (64, 128, 2),
            (128, 256, 2),
            (256, 512, 2),
            (512, 512, 2),
        ],
        'up': [
            (1024, 512, 2, 1),
            (1024, 512, 2, 1),
            (768, 384, 2, 1),
            (512, 256, 2, 1),
            (320, 128, 2, 1),
            (160, 64, 1, 0),
        ],
        'sr_out': 64
    },
    '144': {
        'down': [
            (16, 32, 2),
            (32, 64, 2),
            (64, 128, 2),
            (128, 256, 2),
            (256, 512, 2),
            (512, 512, 2),
        ],
        'up': [
            (1024, 512, 2, 0),
            (1024, 512, 2, 0),
            (768, 384, 2, 1),
            (512, 256, 2, 1),
            (320, 128, 2, 1),
            (160, 64, 2, 1),
        ],
        'sr_out': 64
    },
    '192': {
        'down': [
            (16, 32, 2),
            (32, 64, 2),
            (64, 128, 2),
            (128, 256, 2),
            (256, 512, 2),
            (512, 512, 2),
        ],
        'up': [
            (1024, 512, 2, 1),
            (1024, 512, 2, 1),
            (768, 384, 2, 1),
            (512, 256, 2, 1),
            (320, 128, 2, 1),
            (160, 64, 2, 1),
        ],
        'sr_out': 64
    },
    '256': {
        'down': [
            (16, 32, 2),
            (32, 64, 2),
            (64, 128, 2),
            (128, 256, 2),
            (256, 512, 2),
            (512, 512, 2),
        ],
        'up': [
            (1024, 512, 2, 1),
            (1024, 512, 2, 1),
            (768, 384, 2, 1),
            (512, 256, 2, 1),
            (320, 128, 2, 1),
            (160, 64, 2, 1),
        ],
        'sr_out': 64
    },
    '384': {
        'down': [
            (16, 32, 2),
            (32, 64, 2),
            (64, 128, 2),
            (128, 256, 2),
            (256, 512, 2),
            (512, 512, 2),
            (512, 512, 2),
        ],
        'up': [
            (1024, 512, 2, 1),
            (1024, 512, 2, 1),
            (1024, 512, 2, 1),
            (768, 384, 2, 1),
            (512, 256, 2, 1),
            (320, 128, 2, 1),
            (160, 64, 2, 1),
        ],
        'sr_out': 64
    },
}


def _build(face_size, audio_type):
    if str(face_size) not in face_list:
        f_str = ', '.join(f_size for f_size in face_list.keys())
        raise Exception(f"人脸尺寸不存在！当前支持尺寸：{f_str}")
    if str(audio_type) not in audio_list:
        a_str = ', '.join(a_size for a_size in audio_list.keys())
        raise Exception(f"音频特征类型不存在！当前支持类型：{a_str}")
    audio_shape = audio_list[audio_type]
    downs, ups, sr_out = face_list[str(face_size)]['down'], face_list[str(face_size)]['up'], face_list[str(face_size)][
        'sr_out']
    return downs, ups, sr_out, audio_shape


face_size = int(os.environ.get("WAV2LIPLS_FACE_SIZE", "192"))
audio_type = 'hubert'
downs, ups, sr_out, audio_shape = _build(face_size, audio_type)

class conv2d(nn.Module):
    def __init__(self, in_channels, out_channels, kernel_size=3, stride=1, padding=1, residual=False, num_groups=8):
        super(conv2d, self).__init__()
        self.conv = nn.Conv2d(in_channels, out_channels, kernel_size, stride, padding)
        self.gn = nn.GroupNorm(num_groups=num_groups, num_channels=out_channels)
        self.silu = nn.SiLU()
        self.residual = residual

    def forward(self, x):
        out = self.conv(x)
        if self.residual:
            out = self.gn(out)
            out += x
        out = self.silu(out)
        return out


class conv2d_Transpose(nn.Module):
    def __init__(self, in_channels, out_channels, kernel_size=3, stride=2, padding=1, output_padding=0, num_groups=8):
        super(conv2d_Transpose, self).__init__()
        self.conv = nn.ConvTranspose2d(in_channels, out_channels, kernel_size, stride, padding, output_padding)
        # self.gn = nn.GroupNorm(num_groups=num_groups, num_channels=out_channels)
        self.silu = nn.SiLU()

    def forward(self, x):
        x = self.conv(x)
        # x = self.gn(x)
        x = self.silu(x)
        return x


class FaceEncoder(nn.Module):
    def __init__(self):
        super(FaceEncoder, self).__init__()
        down = []
        for (in_channel, out_channel, stride) in downs:
            down.append(
                nn.Sequential(
                    conv2d(in_channels=in_channel, out_channels=out_channel, kernel_size=3, stride=stride, padding=1),
                    conv2d(out_channel, out_channel, residual=True),
                )
            )
        self.down = nn.ModuleList(down)

    def forward(self, x):
        encode = [x, ]
        for f in self.down:
            x = f(x)
            encode.append(x)
        return encode


class FaceDecoder(nn.Module):
    def __init__(self):
        super(FaceDecoder, self).__init__()
        up = []
        for (in_channel, out_channel, stride, output_padding) in ups:
            up.append(
                nn.Sequential(
                    conv2d_Transpose(in_channels=in_channel, out_channels=out_channel, kernel_size=3, stride=stride,
                                     padding=1,
                                     output_padding=output_padding),
                    conv2d(out_channel, out_channel, residual=True),
                )
            )
        self.up = nn.ModuleList(up)
        self.up_out = nn.Sequential(
            conv2d(ups[-1][1] + downs[0][0], ups[-1][1] + downs[0][0], residual=True),
        )

    def forward(self, face_embedding, audio_embedding):
        x = audio_embedding
        for f in self.up:
            x = f(x)
            try:
                x = torch.cat((x, face_embedding[-1]), dim=1)
            except Exception as e:
                print(x.size())
                print(face_embedding[-1].size())
                raise e
            face_embedding.pop()
        return self.up_out(x)


class AudioBlocks(nn.Module):
    def __init__(self, ndim, seq, hidden_size, out_size):
        super(AudioBlocks, self).__init__()
        self.down = nn.Sequential(
            nn.Conv1d(seq, seq, 3, 1, 1),
            nn.GELU(),
            nn.Conv1d(seq, seq * 2, 3, 2, 1),
            nn.GELU(),
            nn.Conv1d(seq * 2, seq * 4, 3, 2, 1),
            nn.GELU(),
        )
        self.lstm = nn.LSTM(input_size=ndim // 4,
                            hidden_size=hidden_size,
                            num_layers=2,
                            dropout=0.2,
                            bidirectional=False,
                            batch_first=True)
        self.fc = nn.Sequential(
            nn.Conv1d(seq * 4, out_size * out_size, 3, 1, 1),
        )
        self.seq = seq
        self.out_size = out_size

    def forward(self, audio, h0, c0):
        B, T, ndim = audio.size()  # B, 10, 1024 / B, 50, 384
        # down
        if T == 50:
            audio = rearrange(audio, 'b (c1 c2) f -> b c1 (c2 f)', c1=self.seq,
                              c2=T // self.seq)  # B, 10, 1024 / B, 10, 1920
        audio_embedding = self.down(audio)  # B, 40, 256 / B, 40, 384
        # lstm
        audio_embedding, (hn, cn) = self.lstm(audio_embedding, (h0, c0))  # B, 40, 576
        # fc
        audio_embedding = self.fc(audio_embedding)  # B, 9, 512 / # B, 16, 512
        audio_embedding = rearrange(audio_embedding, 'b (h w) c -> b c h w', h=self.out_size,
                                    w=self.out_size)  # B, 512, 3, 3 / # B, 512, 4, 4
        return audio_embedding, hn, cn


class FusionBlocks(nn.Module):
    def __init__(self, in_channels):
        super(FusionBlocks, self).__init__()
        self.audio_norm = nn.LayerNorm(in_channels)  # lN
        self.face_norm = nn.LayerNorm(in_channels)  # lN

    def forward(self, audio, face):
        audio = rearrange(audio, 'b c h w-> b h w c')
        audio = self.audio_norm(audio)
        audio = rearrange(audio, 'b h w c->b c h w')
        face = rearrange(face, 'b c h w-> b h w c')
        face = self.face_norm(face)
        face = rearrange(face, 'b h w c->b c h w')
        x = torch.cat((audio, face), dim=1)
        return x


class Human(nn.Module):
    def __init__(self, sr=False):
        super(Human, self).__init__()
        self.conv_in = nn.Sequential(
            nn.Conv2d(6, downs[0][0], 7, 1, 3),
        )
        self.audioBlocks = AudioBlocks(audio_shape[1], audio_shape[0], 512,
                                       3 if face_size % 3 == 0 else 4)  # input_size, in_channels, hidden_size, out_size
        self.fusionBlocks = FusionBlocks(downs[-1][1])
        self.face_encoder = FaceEncoder()
        self.face_decoder = FaceDecoder()
        self.conv_out_sr = nn.Sequential(
            conv2d_Transpose(in_channels=ups[-1][1] + downs[0][0], out_channels=sr_out, kernel_size=3, stride=2,
                             padding=1,
                             output_padding=1),
            conv2d(sr_out, sr_out, residual=True),
            conv2d(sr_out, sr_out, residual=True),
            nn.Conv2d(sr_out, 3, 3, 1, 1),
            nn.Sigmoid()
        )
        self.conv_out = nn.Sequential(
            nn.Conv2d(ups[-1][1] + downs[0][0], 3, 3, 1, 1),
            nn.Sigmoid()
        )
        self.sr = sr

    def forward(self, audio, face, h0, c0):
        # start
        face = self.conv_in(face)
        # encoder
        feats = self.face_encoder(face)
        # fusion and audio
        audio, hn, cn = self.audioBlocks(audio, h0, c0)
        audio = self.fusionBlocks(audio, feats[-1])
        feats.pop()
        # decoder
        x = self.face_decoder(feats, audio)
        # out
        if self.sr:
            x = self.conv_out_sr(x)
        else:
            x = self.conv_out(x)
        return x, hn, cn


if __name__ == "__main__":
    device = "cuda" if torch.cuda.is_available() else "cpu"
    SR = False
    model = Human(sr=SR).to(device)
    print(model)
    # timer (N, B, H)

    b = hparams.batch_size  # 批次大小
    h0 = torch.zeros(2, b, 512).to(device)
    c0 = torch.zeros(2, b, 512).to(device)
    # hubert (B, 10, 1024)  tiny (B, 50, 384)
    audio = torch.zeros(4, audio_shape[0], audio_shape[1]).to(device)
    # face (B, C, H, W)
    face = torch.zeros(4, 6, face_size, face_size).to(device)
    # 打印模型摘要
    print('total trainable params {}'.format(sum(p.numel() for p in model.parameters() if p.requires_grad)))
    p, h, c = model(audio, face, h0, c0)
    print(p.size(), h.size(), c.size())

    # for i in range(2000):
    #     start_time = time.time()
    #     p, h, c = model(audio, face, h0, c0)
    #     print(i, time.time() - start_time)
