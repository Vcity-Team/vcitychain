package main

import (
	"encoding/hex"
	"fmt"
	"log"

	"github.com/Vcity-Team/vcitychain/bls"
)

func main() {
	// 你提供的BLS私钥（修正长度）
	privateKeyHex := "c9b99a449d79f5a5b1a9238eed250e7e0ad15c5c092e3be9e409f38433713f6"
	
	// 检查并修正私钥长度
	if len(privateKeyHex)%2 != 0 {
		fmt.Printf("⚠️  私钥长度是奇数，尝试修正...\n")
		// 如果长度是奇数，可能缺少前导0
		privateKeyHex = "0" + privateKeyHex
		fmt.Printf("修正后私钥: %s\n", privateKeyHex)
	}
	
	fmt.Printf("🔍 测试BLS密钥生成\n")
	fmt.Printf("私钥 (hex): %s\n", privateKeyHex)
	fmt.Printf("私钥长度: %d 字符 = %d 字节\n", len(privateKeyHex), len(privateKeyHex)/2)
	
	// 直接使用十六进制字符串解析BLS私钥（不需要先解码）
	fmt.Printf("直接使用十六进制字符串解析BLS私钥...\n")
	
	// 解析BLS私钥（直接传入十六进制字符串的字节）
	privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
	if err != nil {
		log.Fatalf("❌ 解析BLS私钥失败: %v", err)
	}
	
	fmt.Printf("✅ BLS私钥解析成功\n")
	
	// 生成公钥
	publicKey := privateKey.PublicKey()
	publicKeyBytes := publicKey.Marshal()
	
	fmt.Printf("🔑 生成的BLS公钥:\n")
	fmt.Printf("公钥字节长度: %d\n", len(publicKeyBytes))
	fmt.Printf("公钥 (hex): %s\n", hex.EncodeToString(publicKeyBytes))
	
	// 你期望的公钥
	expectedPublicKeyHex := "07b459a56fdbc5973a2e73bbbb8702a31a4aa904ccb2749889fbbd2470e4c42e2ce5dbd10dff4cae22b32c694b3ac07b81fb747ee8392f2f8ce1df298220e80e00fe3c6cf66622446f63e75447790495e50cb17c102996fcd50d3eba7821e6df278e6632df99251288448bf4899cf0d8e88112b284966c5cbc3577d60a43d79e"
	
	fmt.Printf("\n🎯 期望的公钥:\n")
	fmt.Printf("期望公钥长度: %d 字符 = %d 字节\n", len(expectedPublicKeyHex), len(expectedPublicKeyHex)/2)
	fmt.Printf("期望公钥 (hex): %s\n", expectedPublicKeyHex)
	
	// 比较结果
	actualPublicKeyHex := hex.EncodeToString(publicKeyBytes)
	if actualPublicKeyHex == expectedPublicKeyHex {
		fmt.Printf("\n✅ 公钥匹配！生成正确！\n")
	} else {
		fmt.Printf("\n❌ 公钥不匹配！\n")
		fmt.Printf("实际: %s\n", actualPublicKeyHex)
		fmt.Printf("期望: %s\n", expectedPublicKeyHex)
		
		// 分析差异
		if len(actualPublicKeyHex) != len(expectedPublicKeyHex) {
			fmt.Printf("长度差异: 实际=%d, 期望=%d\n", len(actualPublicKeyHex), len(expectedPublicKeyHex))
		}
	}
	
	// 测试私钥的Marshal方法
	marshaledPrivateKey, err := privateKey.Marshal()
	if err != nil {
		log.Fatalf("❌ Marshal私钥失败: %v", err)
	}
	
	fmt.Printf("\n🔍 私钥Marshal结果:\n")
	fmt.Printf("Marshal长度: %d\n", len(marshaledPrivateKey))
	fmt.Printf("Marshal内容: %s\n", string(marshaledPrivateKey))
	
	// 测试从Marshal结果重新解析
	reparsedPrivateKey, err := bls.UnmarshalPrivateKey(marshaledPrivateKey)
	if err != nil {
		log.Fatalf("❌ 重新解析私钥失败: %v", err)
	}
	
	reparsedPublicKey := reparsedPrivateKey.PublicKey()
	reparsedPublicKeyBytes := reparsedPublicKey.Marshal()
	reparsedPublicKeyHex := hex.EncodeToString(reparsedPublicKeyBytes)
	
	fmt.Printf("\n🔄 重新解析后的公钥:\n")
	fmt.Printf("重新解析公钥: %s\n", reparsedPublicKeyHex)
	
	if reparsedPublicKeyHex == actualPublicKeyHex {
		fmt.Printf("✅ 重新解析成功，公钥一致\n")
	} else {
		fmt.Printf("❌ 重新解析失败，公钥不一致\n")
	}
}
