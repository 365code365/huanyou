package profile

// cnDomains are mainland-China services that are reached directly and
// resolved by a domestic DNS-over-HTTPS server, in the modes that route China
// directly (智能 / 集群).
//
// Why: routing is by address (the China IP list), and names are otherwise
// resolved abroad through the tunnel. Many large Chinese sites answer a
// foreign resolver with their overseas CDN, which is not in the China list:
// Taobao images, Bilibili video etc. then went to the node, abroad and back
// (slow, and counted against the user's traffic). These names are resolved
// at home and connected to directly. Nothing new is revealed by that: the
// connection itself goes direct anyway, so the local network sees it.
//
// Only unambiguous, high-traffic domestic services; the long tail still goes
// direct through the IP list whenever it resolves to a Chinese address.
var cnDomains = []string{
	"cn", // every .cn name (com.cn, gov.cn, edu.cn …)
	// search, portals, news
	"baidu.com", "bdstatic.com", "bdimg.com", "baidupcs.com", "baidustatic.com", "bcebos.com",
	"sogou.com", "so.com", "360.com", "qhimg.com", "qhres2.com",
	"163.com", "126.net", "127.net", "netease.com", "sohu.com", "sohucs.com", "ifeng.com", "toutiao.com",
	// Tencent
	"qq.com", "gtimg.com", "idqqimg.com", "qlogo.cn", "tencent.com", "tenpay.com", "myqcloud.com", "qcloud.com",
	"tencent-cloud.net", "weixinbridge.com", "wechat.com",
	// Alibaba, Ant
	"taobao.com", "tmall.com", "alicdn.com", "alipay.com", "alipayobjects.com", "aliyun.com", "aliyuncs.com",
	"alibabausercontent.com", "tbcdn.com", "mmstat.com", "amap.com", "autonavi.com", "dingtalk.com", "ele.me",
	"youku.com", "ykimg.com", "1688.com", "fliggy.com", "uc.cn", "xiami.com",
	// ByteDance
	"douyin.com", "douyinpic.com", "douyincdn.com", "douyinvod.com", "douyinstatic.com", "snssdk.com",
	"pstatp.com", "byteimg.com", "bytecdn.com", "ixigua.com", "toutiaoimg.com", "zijieapi.com", "feishu.cn",
	// shopping, local services, travel
	"jd.com", "360buyimg.com", "jdcloud.com", "pinduoduo.com", "yangkeduo.com", "pddpic.com", "meituan.com",
	"meituan.net", "dianping.com", "vip.com", "suning.com", "dangdang.com", "smzdm.com", "ctrip.com", "qunar.com",
	"tuniu.com", "58.com", "anjuke.com", "ke.com", "lianjia.com", "zhipin.com", "51job.com",
	// video, music, social
	"bilibili.com", "hdslb.com", "bilivideo.com", "biliapi.net", "iqiyi.com", "iqiyipic.com", "qy.net",
	"mgtv.com", "kuaishou.com", "yximgs.com", "huya.com", "douyu.com", "weibo.com", "sinaimg.cn", "zhihu.com",
	"zhimg.com", "douban.com", "doubanio.com", "xiaohongshu.com", "xhscdn.com", "kugou.com", "kuwo.cn",
	"ximalaya.com", "csdn.net", "jianshu.com",
	// phones, banks, carriers
	"xiaomi.com", "mi.com", "huawei.com", "hicloud.com", "vmall.com", "oppo.com", "heytap.com", "vivo.com",
	"unionpay.com", "95516.com", "cmbchina.com", "ccb.com", "abchina.com", "icbc.com.cn", "10010.com", "10086.cn",
}
