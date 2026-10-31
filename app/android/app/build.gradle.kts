import java.util.Properties

plugins {
    id("com.android.application")
    id("kotlin-android")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

// Release signing (android/key.properties + android/keystore/, both kept out
// of git). It is the key every installed copy is signed with: Android only
// updates an app signed by the same key, and ANDROID_ID — the device
// account's identity — is tied to it. Back both files up offline.
val keyProps = Properties().apply {
    val f = rootProject.file("key.properties")
    if (f.exists()) f.inputStream().use { load(it) }
}

android {
    namespace = "com.vpnplatform.vpn_app"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = flutter.ndkVersion

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = JavaVersion.VERSION_17.toString()
    }

    defaultConfig {
        // TODO: Specify your own unique Application ID (https://developer.android.com/studio/build/application-id.html).
        applicationId = "com.vpnplatform.vpn_app"
        // You can update the following values to match your application needs.
        // For more information, see: https://flutter.dev/to/review-gradle-config.
        minSdk = 24
        // vpncore.aar is built with -androidapi 24 for arm64 + 32-bit arm only
        // (see mobile/build.sh); build with --target-platform android-arm,android-arm64.
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName
    }

    // Store native libraries compressed: the download is roughly half the size
    // (the Go core and Flutter engine compress well); Android extracts them once
    // at install time.
    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }

    signingConfigs {
        if (keyProps.isNotEmpty()) {
            create("release") {
                storeFile = rootProject.file(keyProps.getProperty("storeFile"))
                storePassword = keyProps.getProperty("storePassword")
                keyAlias = keyProps.getProperty("keyAlias")
                keyPassword = keyProps.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        release {
            // Without key.properties (another machine) it falls back to the
            // debug key; scripts/build_android.sh refuses to ship that.
            signingConfig = signingConfigs.findByName("release") ?: signingConfigs.getByName("debug")
            // R8: shrink and obfuscate the Kotlin / Java side too.
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }
}

flutter {
    source = "../.."
}

dependencies {
    // Xray-core client, built by mobile/build.sh android
    implementation(files("libs/vpncore.aar"))
}
