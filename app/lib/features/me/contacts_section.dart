import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:font_awesome_flutter/font_awesome_flutter.dart';

import '../../core/api.dart';
import '../../core/open_url.dart';
import '../../theme/app_theme.dart';
import '../../i18n/i18n.dart';

/// Brand icon + colour per contact type (types come from the admin panel).
(FaIconData, Color) contactStyle(String type) => switch (type) {
      'telegram' => (FontAwesomeIcons.telegram, const Color(0xFF229ED9)),
      'x' => (FontAwesomeIcons.xTwitter, const Color(0xFF111111)),
      'instagram' => (FontAwesomeIcons.instagram, const Color(0xFFE1306C)),
      'facebook' => (FontAwesomeIcons.facebook, const Color(0xFF1877F2)),
      'youtube' => (FontAwesomeIcons.youtube, const Color(0xFFFF0000)),
      'tiktok' => (FontAwesomeIcons.tiktok, const Color(0xFF111111)),
      'discord' => (FontAwesomeIcons.discord, const Color(0xFF5865F2)),
      'whatsapp' => (FontAwesomeIcons.whatsapp, const Color(0xFF25D366)),
      'wechat' => (FontAwesomeIcons.weixin, const Color(0xFF07C160)),
      'qq' => (FontAwesomeIcons.qq, const Color(0xFF12B7F5)),
      'email' => (FontAwesomeIcons.solidEnvelope, AppColors.ink2),
      _ => (FontAwesomeIcons.globe, AppColors.ink2),
    };

/// Opens the contact's link, or copies its value when there is no link (微信 / QQ).
Future<void> openContact(BuildContext context, Contact c) async {
  final messenger = ScaffoldMessenger.of(context);
  if (c.link.isNotEmpty && await openUrl(Uri.parse(c.link))) return;
  await Clipboard.setData(ClipboardData(text: c.value));
  HapticFeedback.lightImpact();
  messenger.showSnackBar(SnackBar(content: Text(tr('{0}「{1}」已复制', [c.name, c.value]))));
}

/// "联系客服" rows + "关注我们" icon strip. Renders nothing if none configured.
class ContactsSection extends StatelessWidget {
  const ContactsSection({super.key, required this.contacts});
  final List<Contact> contacts;

  @override
  Widget build(BuildContext context) {
    final service = contacts.where((c) => c.group == 'service').toList();
    final social = contacts.where((c) => c.group == 'social').toList();
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      if (service.isNotEmpty) ...[
        _Heading(tr('联系客服')),
        Card(
          clipBehavior: Clip.antiAlias,
          child: Column(children: [
            for (var i = 0; i < service.length; i++) ...[
              if (i > 0) const Divider(indent: 66),
              _ServiceRow(contact: service[i]),
            ],
          ]),
        ),
      ],
      if (social.isNotEmpty) ...[
        _Heading(tr('关注我们')),
        Card(
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 14),
            child: Wrap(
              alignment: WrapAlignment.spaceEvenly,
              runSpacing: 12,
              children: [for (final c in social) _SocialChip(contact: c)],
            ),
          ),
        ),
      ],
    ]);
  }
}

class _Heading extends StatelessWidget {
  const _Heading(this.text);
  final String text;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.fromLTRB(6, 18, 6, 8),
        child: Text(text, style: TextStyle(fontSize: 13, color: AppColors.sub, fontWeight: FontWeight.w600)),
      );
}

class _ServiceRow extends StatelessWidget {
  const _ServiceRow({required this.contact});
  final Contact contact;

  @override
  Widget build(BuildContext context) {
    final (icon, color) = contactStyle(contact.type);
    final copyOnly = contact.link.isEmpty;
    return InkWell(
      key: Key('contact-${contact.id}'),
      onTap: () => openContact(context, contact),
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 13),
        child: Row(children: [
          Container(
            width: 34,
            height: 34,
            decoration: BoxDecoration(color: color.withValues(alpha: 0.1), borderRadius: BorderRadius.circular(10)),
            alignment: Alignment.center,
            child: FaIcon(icon, size: 17, color: color),
          ),
          const SizedBox(width: 14),
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(contact.name, style: const TextStyle(fontSize: 15)),
              if (contact.desc.isNotEmpty || contact.value.isNotEmpty)
                Text(contact.desc.isNotEmpty ? contact.desc : contact.value,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(fontSize: 12, color: AppColors.sub)),
            ]),
          ),
          Icon(copyOnly ? Icons.copy_rounded : Icons.open_in_new_rounded, size: 17, color: AppColors.faint),
        ]),
      ),
    );
  }
}

class _SocialChip extends StatelessWidget {
  const _SocialChip({required this.contact});
  final Contact contact;

  @override
  Widget build(BuildContext context) {
    final (icon, color) = contactStyle(contact.type);
    return InkWell(
      key: Key('contact-${contact.id}'),
      borderRadius: BorderRadius.circular(14),
      onTap: () => openContact(context, contact),
      child: SizedBox(
        width: 72,
        child: Column(children: [
          Container(
            width: 46,
            height: 46,
            decoration: BoxDecoration(color: color.withValues(alpha: 0.1), shape: BoxShape.circle),
            alignment: Alignment.center,
            child: FaIcon(icon, size: 21, color: color),
          ),
          const SizedBox(height: 6),
          Text(contact.name,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(fontSize: 12, color: AppColors.sub)),
        ]),
      ),
    );
  }
}
